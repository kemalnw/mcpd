package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kemalnw/mcpd/internal/audit"
	"github.com/kemalnw/mcpd/internal/config"
	durablemgr "github.com/kemalnw/mcpd/internal/durableexec"
	fsmgr "github.com/kemalnw/mcpd/internal/filesystem"
	oauthsrv "github.com/kemalnw/mcpd/internal/oauth"
	processmgr "github.com/kemalnw/mcpd/internal/process"
	searchmgr "github.com/kemalnw/mcpd/internal/search"
	"github.com/kemalnw/mcpd/internal/tools"
	"github.com/kemalnw/mcpd/internal/version"
	workflowmgr "github.com/kemalnw/mcpd/internal/workflow"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type App struct {
	cfg              config.Config
	logger           *slog.Logger
	audit            *audit.Store
	processes        *processmgr.Manager
	files            *fsmgr.Manager
	searches         *searchmgr.Manager
	workflows        *workflowmgr.Store
	workflowGCCancel context.CancelFunc
	workflowGCDone   chan struct{}
	durable          *durablemgr.Manager
	oauth            *oauthsrv.Server
	mcp              *mcp.Server
	http             *http.Server
}

func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	workflowStore, err := workflowmgr.Open(cfg.Workflow.StateDir)
	if err != nil {
		return nil, err
	}
	durableRoot := filepath.Join(filepath.Dir(cfg.Workflow.StateDir), "durable")
	durableManager, err := durablemgr.Open(durableRoot, durablemgr.SupervisorSocket(durableRoot))
	if err != nil {
		return nil, fmt.Errorf("initialize durable execution manager: %w", err)
	}
	reconciledJobs, err := durableManager.Reconcile()
	if err != nil {
		return nil, fmt.Errorf("reconcile durable execution state: %w", err)
	}
	if len(reconciledJobs) > 0 {
		logger.Info("durable execution state reconciled", "job_count", len(reconciledJobs))
	}
	auditStore, err := audit.Open(cfg.Audit.Enabled, cfg.Audit.Path)
	if err != nil {
		return nil, err
	}
	processes, err := processmgr.NewManager(processmgr.Options{
		DefaultShell: cfg.Process.DefaultShell, DefaultWaitMS: cfg.Process.DefaultWaitMS,
		InitialOutputLines: cfg.Process.InitialOutputLines, ResponseOutputBytes: cfg.Process.ResponseOutputBytes, FailureTailLines: cfg.Process.FailureTailLines, OutputBufferBytes: cfg.Process.OutputBufferBytes,
		MaxLineBytes: cfg.Process.MaxLineBytes, CompletedSessions: cfg.Process.CompletedSessions, BatchMaxParallel: cfg.Process.BatchMaxParallel, BatchGlobalParallel: cfg.Process.BatchGlobalParallel,
	})
	if err != nil {
		_ = auditStore.Close()
		return nil, err
	}
	files, err := fsmgr.NewManager(fsmgr.Options{
		DefaultReadLines: cfg.Files.DefaultReadLines, MaxLineBytes: cfg.Files.MaxLineBytes,
		NestedEntryLimit: cfg.Files.NestedEntryLimit, HTTPTimeout: time.Duration(cfg.Files.HTTPTimeoutSeconds) * time.Second,
		MaxRemoteBytes: cfg.Files.MaxRemoteBytes,
	})
	if err != nil {
		_ = processes.Close()
		_ = auditStore.Close()
		return nil, err
	}
	workspaceRoots := discoverWorkspaceRoots()
	searches, err := searchmgr.NewManager(searchmgr.ManagerOptions{
		DefaultMaxResults: cfg.Search.DefaultMaxResults, Retention: time.Duration(cfg.Search.RetentionSeconds) * time.Second,
		InitialWait:    time.Duration(cfg.Search.InitialWaitMS) * time.Millisecond,
		PreferredRoots: workspaceRoots,
	})
	if err != nil {
		_ = processes.Close()
		_ = auditStore.Close()
		return nil, err
	}
	cleanup := func() {
		_ = searches.Close()
		_ = processes.Close()
		_ = auditStore.Close()
	}

	var authServer *oauthsrv.Server
	if cfg.Auth.Enabled {
		authServer, err = oauthsrv.NewServer(oauthsrv.Options{
			IssuerURL: cfg.Auth.ExternalURL, ResourceURL: cfg.Auth.ExternalURL + cfg.Server.MCPPath, StateDir: cfg.Auth.StateDir,
			AccessTokenTTL:        time.Duration(cfg.Auth.AccessTokenSeconds) * time.Second,
			RefreshTokenIdleTTL:   time.Duration(cfg.Auth.RefreshTokenIdleSeconds) * time.Second,
			AuthorizationCodeTTL:  time.Duration(cfg.Auth.AuthorizationCodeSeconds) * time.Second,
			LoginSessionTTL:       time.Duration(cfg.Auth.LoginSessionSeconds) * time.Second,
			ClientMetadataTimeout: time.Duration(cfg.Auth.ClientMetadataTimeoutSeconds) * time.Second,
			Logger:                logger,
		})
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("initialize OAuth server: %w", err)
		}
	}

	v := version.Current()
	catalogFingerprint := tools.CatalogFingerprint()
	instructions := `MCPD operates the connected Linux VM with the permissions of the daemon user.
Choose the narrowest dedicated tool that directly matches the task; use start_process only when shell execution is actually needed.
For files: use list_directory to browse a known directory, start_search to discover filenames or content, read_file/read_multiple_files to read known paths, get_file_info for metadata, edit_block for localized edits, and write_file for full rewrites/creation/appends. Append retries should carry expected_size; after an ambiguous move_file response verify source/destination before retrying.
For commands: use start_process once, then continue that PID with read_process_output or interact_with_process. Prefer start_process_batch for 2+ independent non-interactive commands and continue the batch with changed-only read_process_batch. For transport retries of non-repeatable work, reuse start_process.idempotency_key or interact_with_process.operation_key instead of issuing a fresh side effect. Prefer force_terminate for MCPD-managed PIDs; for arbitrary kill_process retries use expected_start_ticks from list_processes so PID reuse is rejected.
For searches: continue an existing search with get_more_search_results instead of launching a duplicate search. When the user names a project/repository but its exact path is unknown, pass that name as start_search.pathHint and search a likely workspace root instead of retrying progressively broader roots.
For long engineering workflows: use durable runs. A fresh agent/session with a run_id should call resume_run instead of replaying chat history. If checkpoint_due is true, checkpoint promptly; call handoff_run before long waits or an anticipated session/turn/context boundary. For non-interactive commands that must continue across MCPD daemon restarts, use start_durable_job and persist its job_id in the run checkpoint; inspect it with get_durable_job/read_durable_job_log instead of restarting expensive work merely because the supervising client disconnected.
Read-only inspection should precede mutation when target paths, PIDs, or current state are uncertain. Avoid unnecessary tool calls and batch independent reads when practical.`
	instructions += fmt.Sprintf("\nMCPD tool catalog version: %d (%s). If a client lacks tools/fields expected for this catalog after an upgrade, verify a fresh tools/list and reconnect/reload the client.", tools.CatalogVersion, catalogFingerprint)
	if len(workspaceRoots) > 0 {
		instructions += "\nKnown workspace roots on this VM: " + strings.Join(workspaceRoots, ", ") + ". Prefer these for project/repository searches before the entire home directory."
	}
	serverCapabilities := &mcp.ServerCapabilities{}
	serverCapabilities.AddExtension(tools.TasksExtensionID, nil)
	server := mcp.NewServer(&mcp.Implementation{
		Name:        "mcpd",
		Title:       "MCPD",
		Description: "Remote Linux development and server operations through precise process, filesystem, and search tools.",
		Version:     v.Version,
		WebsiteURL:  "https://github.com/kemalnw/mcpd",
	}, &mcp.ServerOptions{
		Logger:       logger,
		Capabilities: serverCapabilities,
		Instructions: instructions,
	})
	tools.RegisterProcess(server, processes, auditStore)
	tools.RegisterFilesystem(server, files, auditStore)
	tools.RegisterSearch(server, searches, auditStore)
	tools.RegisterWorkflow(server, workflowStore, auditStore, time.Duration(cfg.Workflow.CheckpointIntervalSeconds)*time.Second, time.Duration(cfg.Workflow.CompletedRetentionSeconds)*time.Second, cfg.Workflow.GarbageCollectMaxDeletes)
	tools.RegisterDurable(server, durableManager, auditStore)
	if err := tools.RegisterTasksExtension(server, durableManager); err != nil {
		return nil, fmt.Errorf("register MCP Tasks extension: %w", err)
	}

	streamableOpts := &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: logger,
		PropagateRequestCancellation: true,
	}
	if authServer != nil {
		// mcpd is intentionally bound to loopback behind the managed TLS reverse proxy.
		// The SDK's localhost DNS-rebinding guard would reject the canonical public
		// Host in that topology, so mcpd replaces it with an exact canonical-host
		// check before traffic reaches the SDK.
		streamableOpts.DisableLocalhostProtection = true
	}
	mcpHandler := http.Handler(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, streamableOpts))
	if authServer != nil {
		server.AddReceivingMiddleware(oauthsrv.EnforceToolScopes(authServer, tools.RequiredScope))
		mcpHandler = oauthsrv.InjectToolSecuritySchemes(mcpHandler, tools.RequiredScope, logger)
		mcpHandler = oauthsrv.ProtectMCP(mcpHandler, authServer)
		mcpHandler, err = enforceCanonicalHost(mcpHandler, cfg.Auth.ExternalURL, logger)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("configure MCP canonical host validation: %w", err)
		}
	}

	mux := http.NewServeMux()
	mux.Handle("POST "+cfg.Server.MCPPath, mcpHandler)
	if authServer != nil {
		mux.HandleFunc("GET /.well-known/oauth-protected-resource", authServer.ProtectedResourceMetadata)
		if cfg.Server.MCPPath != "/" {
			mux.HandleFunc("GET /.well-known/oauth-protected-resource"+cfg.Server.MCPPath, authServer.ProtectedResourceMetadata)
		}
		mux.HandleFunc("GET /.well-known/oauth-authorization-server", authServer.AuthorizationServerMetadata)
		// Some OAuth clients probe OpenID discovery as a compatibility fallback.
		// Return the OAuth authorization-server metadata without advertising
		// openid scope or ID-token support; mcpd remains an OAuth AS, not an OIDC OP.
		mux.HandleFunc("GET /.well-known/openid-configuration", authServer.AuthorizationServerMetadata)
		mux.HandleFunc("GET /oauth/jwks.json", authServer.JWKS)
		mux.HandleFunc("GET /oauth/authorize", authServer.Authorize)
		mux.HandleFunc("POST /oauth/authorize", authServer.Authorize)
		mux.HandleFunc("POST /oauth/token", authServer.Token)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": v.Version, "tool_catalog_version": tools.CatalogVersion, "tool_catalog_fingerprint": catalogFingerprint})
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "mcpd", "version": v.Version, "mcp": cfg.Server.MCPPath, "oauth": authServer != nil, "tool_catalog_version": tools.CatalogVersion, "tool_catalog_fingerprint": catalogFingerprint})
	})

	gcCtx, gcCancel := context.WithCancel(context.Background())
	gcDone := make(chan struct{})
	go runWorkflowGarbageCollector(gcCtx, gcDone, workflowStore, logger, time.Duration(cfg.Workflow.GarbageCollectIntervalSeconds)*time.Second, time.Duration(cfg.Workflow.CompletedRetentionSeconds)*time.Second, cfg.Workflow.GarbageCollectMaxDeletes)
	a := &App{cfg: cfg, logger: logger, audit: auditStore, processes: processes, files: files, searches: searches, workflows: workflowStore, workflowGCCancel: gcCancel, workflowGCDone: gcDone, durable: durableManager, oauth: authServer, mcp: server}
	a.http = &http.Server{
		Addr: cfg.Server.Listen, Handler: accessLog(logger, mux),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second,
	}
	return a, nil
}

func (a *App) Run() error {
	a.logger.Info("mcpd starting", "listen", a.cfg.Server.Listen, "mcp_path", a.cfg.Server.MCPPath, "version", version.Current().Version,
		"oauth", a.oauth != nil, "transport", "http")
	if err := a.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func (a *App) Shutdown(ctx context.Context) error {
	var first error
	if a.workflowGCCancel != nil {
		a.workflowGCCancel()
	}
	if a.workflowGCDone != nil {
		select {
		case <-a.workflowGCDone:
		case <-ctx.Done():
			if first == nil {
				first = ctx.Err()
			}
		}
	}
	if err := a.http.Shutdown(ctx); err != nil {
		first = err
	}
	if err := a.searches.Close(); err != nil && first == nil {
		first = err
	}
	if err := a.processes.Close(); err != nil && first == nil {
		first = err
	}
	if err := a.audit.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

func runWorkflowGarbageCollector(ctx context.Context, done chan<- struct{}, store *workflowmgr.Store, logger *slog.Logger, interval, retention time.Duration, maxDeletes int) {
	defer close(done)
	collect := func() {
		result, err := store.CollectGarbage(workflowmgr.GCPolicy{CompletedRetention: retention, MaxDeletes: maxDeletes})
		if err != nil {
			logger.Error("workflow garbage collection failed", "error", err)
			return
		}
		if result.DeletedRuns > 0 || result.PrunedIdempotencyRecords > 0 || result.PrunedExpiredLeases > 0 || result.CleanedTrashEntries > 0 {
			logger.Info("workflow garbage collection completed", "deleted_runs", result.DeletedRuns, "deleted_bytes", result.DeletedBytes, "pruned_idempotency_records", result.PrunedIdempotencyRecords, "pruned_expired_leases", result.PrunedExpiredLeases, "cleaned_trash_entries", result.CleanedTrashEntries)
		}
	}
	collect()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			collect()
		}
	}
}

type responseMetricsWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseMetricsWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseMetricsWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseMetricsWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func accessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		metrics := &responseMetricsWriter{ResponseWriter: w}
		next.ServeHTTP(metrics, r)
		status := metrics.status
		if status == 0 {
			status = http.StatusOK
		}
		logger.Info("http request",
			"method", r.Method, "path", r.URL.Path, "host", r.Host, "remote", r.RemoteAddr,
			"status", status, "response_bytes", metrics.bytes, "duration_ms", time.Since(started).Milliseconds())
	})
}

func discoverWorkspaceRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	names := []string{"src", "workspace", "work", "projects", "project", "code", "repos", "repositories", "dev", "development"}
	roots := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(home, name)
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			roots = append(roots, path)
		}
	}
	return roots
}

func enforceCanonicalHost(next http.Handler, externalURL string, logger *slog.Logger) (http.Handler, error) {
	u, err := url.Parse(externalURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid external URL %q", externalURL)
	}
	expected := u.Host
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Host, expected) {
			logger.Warn("mcp host rejected", "host", r.Host, "expected_host", expected)
			http.Error(w, "forbidden: invalid Host header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}), nil
}
