package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/kemalnw/mcpd/internal/audit"
	"github.com/kemalnw/mcpd/internal/config"
	fsmgr "github.com/kemalnw/mcpd/internal/filesystem"
	oauthsrv "github.com/kemalnw/mcpd/internal/oauth"
	processmgr "github.com/kemalnw/mcpd/internal/process"
	searchmgr "github.com/kemalnw/mcpd/internal/search"
	"github.com/kemalnw/mcpd/internal/tools"
	"github.com/kemalnw/mcpd/internal/version"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type App struct {
	cfg       config.Config
	logger    *slog.Logger
	audit     *audit.Store
	processes *processmgr.Manager
	files     *fsmgr.Manager
	searches  *searchmgr.Manager
	oauth     *oauthsrv.Server
	mcp       *mcp.Server
	http      *http.Server
}

func New(cfg config.Config, logger *slog.Logger) (*App, error) {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	auditStore, err := audit.Open(cfg.Audit.Enabled, cfg.Audit.Path)
	if err != nil {
		return nil, err
	}
	processes, err := processmgr.NewManager(processmgr.Options{
		DefaultShell: cfg.Process.DefaultShell, DefaultWaitMS: cfg.Process.DefaultWaitMS,
		OutputBufferBytes: cfg.Process.OutputBufferBytes, MaxLineBytes: cfg.Process.MaxLineBytes,
		CompletedSessions: cfg.Process.CompletedSessions,
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
	searches, err := searchmgr.NewManager(searchmgr.ManagerOptions{
		DefaultMaxResults: cfg.Search.DefaultMaxResults, Retention: time.Duration(cfg.Search.RetentionSeconds) * time.Second,
		InitialWait: time.Duration(cfg.Search.InitialWaitMS) * time.Millisecond,
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
	server := mcp.NewServer(&mcp.Implementation{Name: "mcpd", Version: v.Version}, &mcp.ServerOptions{
		Logger:       logger,
		Capabilities: &mcp.ServerCapabilities{},
		Instructions: "mcpd provides direct Linux VM process and filesystem capabilities using the permissions of the daemon user.",
	})
	tools.RegisterProcess(server, processes, auditStore)
	tools.RegisterFilesystem(server, files, auditStore)
	tools.RegisterSearch(server, searches, auditStore)

	mcpHandler := http.Handler(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: logger,
		PropagateRequestCancellation: true,
	}))
	if authServer != nil {
		server.AddReceivingMiddleware(oauthsrv.EnforceToolScopes(authServer, tools.RequiredScope))
		mcpHandler = oauthsrv.InjectToolSecuritySchemes(mcpHandler, tools.RequiredScope)
		mcpHandler = oauthsrv.ProtectMCP(mcpHandler, authServer)
	}

	mux := http.NewServeMux()
	mux.Handle("POST "+cfg.Server.MCPPath, mcpHandler)
	if authServer != nil {
		mux.HandleFunc("GET /.well-known/oauth-protected-resource", authServer.ProtectedResourceMetadata)
		if cfg.Server.MCPPath != "/" {
			mux.HandleFunc("GET /.well-known/oauth-protected-resource"+cfg.Server.MCPPath, authServer.ProtectedResourceMetadata)
		}
		mux.HandleFunc("GET /.well-known/oauth-authorization-server", authServer.AuthorizationServerMetadata)
		mux.HandleFunc("GET /oauth/jwks.json", authServer.JWKS)
		mux.HandleFunc("GET /oauth/authorize", authServer.Authorize)
		mux.HandleFunc("POST /oauth/authorize", authServer.Authorize)
		mux.HandleFunc("POST /oauth/token", authServer.Token)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "version": v.Version})
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": "mcpd", "version": v.Version, "mcp": cfg.Server.MCPPath, "oauth": authServer != nil})
	})

	a := &App{cfg: cfg, logger: logger, audit: auditStore, processes: processes, files: files, searches: searches, oauth: authServer, mcp: server}
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

func accessLog(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("http request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "duration_ms", time.Since(started).Milliseconds())
	})
}
