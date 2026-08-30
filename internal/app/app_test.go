//go:build linux

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kemalnw/mcpd/internal/config"
	oauthsrv "github.com/kemalnw/mcpd/internal/oauth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStatelessMCPEndToEnd(t *testing.T) {
	cfg := config.Default()
	cfg.Audit.Path = filepath.Join(t.TempDir(), "audit.jsonl")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer application.searches.Close()
	defer application.processes.Close()
	defer application.audit.Close()

	httpServer := httptest.NewServer(application.http.Handler)
	defer httpServer.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpd-test", Version: "dev"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + cfg.Server.MCPPath, HTTPClient: httpServer.Client(), DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	init := session.InitializeResult()
	if init == nil || init.ProtocolVersion != "2026-07-28" {
		t.Fatalf("protocol = %#v", init)
	}
	if init.ServerInfo == nil || init.ServerInfo.Name != "mcpd" || init.ServerInfo.Title != "MCPD" {
		t.Fatalf("server info = %#v", init.ServerInfo)
	}
	if init.ServerInfo.Description == "" || init.ServerInfo.WebsiteURL != "https://github.com/kemalnw/mcpd" {
		t.Fatalf("server metadata = %#v", init.ServerInfo)
	}
	if !strings.Contains(init.Instructions, "narrowest dedicated tool") || !strings.Contains(init.Instructions, "continue that PID") || !strings.Contains(init.Instructions, "pathHint") || !strings.Contains(init.Instructions, "start_durable_job") {
		t.Fatalf("server instructions missing tool-selection guidance: %q", init.Instructions)
	}
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{
		"start_process": false, "read_process_output": false, "interact_with_process": false,
		"force_terminate": false, "list_sessions": false, "list_processes": false, "kill_process": false,
		"read_file": false, "read_multiple_files": false, "write_file": false, "create_directory": false,
		"list_directory": false, "move_file": false, "get_file_info": false, "edit_block": false,
		"start_search": false, "get_more_search_results": false, "stop_search": false, "list_searches": false,
		"start_durable_job": false, "get_durable_job": false, "list_durable_jobs": false, "read_durable_job_log": false, "cancel_durable_job": false,
	}
	byName := make(map[string]*mcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		byName[tool.Name] = tool
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	assertToolHints := func(name string, readOnly, destructive, idempotent, openWorld bool) {
		t.Helper()
		tool := byName[name]
		if tool == nil || tool.Annotations == nil {
			t.Fatalf("tool %q missing annotations", name)
		}
		got := tool.Annotations
		if got.DestructiveHint == nil || got.OpenWorldHint == nil || got.ReadOnlyHint != readOnly || *got.DestructiveHint != destructive || got.IdempotentHint != idempotent || *got.OpenWorldHint != openWorld {
			t.Fatalf("tool %q annotations = %#v", name, got)
		}
		if tool.Title == "" || tool.Description == "" {
			t.Fatalf("tool %q missing title/description", name)
		}
	}
	assertToolHints("start_process", false, true, false, true)
	assertToolHints("read_file", true, false, false, true)
	assertToolHints("list_directory", true, false, true, false)
	assertToolHints("create_directory", false, false, true, false)
	assertToolHints("kill_process", false, true, false, false)
	assertToolHints("get_more_search_results", true, false, true, false)
	assertToolHints("start_durable_job", false, true, false, true)
	assertToolHints("get_durable_job", true, false, true, false)
	assertToolHints("list_durable_jobs", true, false, true, false)
	assertToolHints("read_durable_job_log", true, false, true, false)
	assertToolHints("cancel_durable_job", false, true, true, false)
	for name, found := range required {
		if !found {
			t.Errorf("required tool %q not listed", name)
		}
	}

	processCWD := t.TempDir()
	processResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "start_process", Arguments: map[string]any{"command": "printf 'mcpd-e2e\\n'", "cwd": processCWD, "timeout_ms": 1000, "pty": "never"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertToolOK(t, processResult)
	processStructured := requireStructuredMap(t, processResult)
	processPID, ok := processStructured["pid"].(float64)
	if !ok || processPID <= 0 {
		t.Fatalf("start_process returned invalid pid: %#v", processStructured)
	}
	if processStructured["cwd"] != processCWD {
		t.Fatalf("start_process did not preserve cwd: %#v", processStructured)
	}
	processDeadline := time.Now().Add(5 * time.Second)
	for processStructured["state"] != "exited" {
		if time.Now().After(processDeadline) {
			t.Fatalf("process did not exit: %#v", processStructured)
		}
		readProcessResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "read_process_output", Arguments: map[string]any{
				"pid": int(processPID), "timeout_ms": 250, "offset": 0, "length": 100,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertToolOK(t, readProcessResult)
		processStructured = requireStructuredMap(t, readProcessResult)
	}

	path := filepath.Join(t.TempDir(), "mcp.txt")
	writeResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "write_file", Arguments: map[string]any{"path": path, "content": "alpha\nbeta\n", "mode": "rewrite"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertToolOK(t, writeResult)

	readResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "read_file", Arguments: map[string]any{"path": path, "offset": 1, "length": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertToolOK(t, readResult)
	readStructured := requireStructuredMap(t, readResult)
	if _, duplicated := readStructured["content"]; duplicated {
		t.Fatalf("local read unexpectedly included duplicate content: %#v", readStructured)
	}
	lines, ok := readStructured["lines"].([]any)
	if !ok || len(lines) != 1 || lines[0] != "beta" || readStructured["total_lines"] != float64(2) {
		t.Fatalf("unexpected read output: %#v", readStructured)
	}

	newText := "BETA"
	editResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "edit_block", Arguments: map[string]any{"file_path": path, "old_string": "beta", "new_string": newText, "expected_replacements": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertToolOK(t, editResult)
	editStructured := requireStructuredMap(t, editResult)
	if editStructured["applied"] != true {
		t.Fatalf("unexpected edit output: %#v", editStructured)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha\nBETA\n" {
		t.Fatalf("edited file = %q", data)
	}

	infoResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get_file_info", Arguments: map[string]any{"path": path},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertToolOK(t, infoResult)
	infoStructured := requireStructuredMap(t, infoResult)
	if infoStructured["file_type"] != "text" || infoStructured["line_count"] != float64(2) {
		t.Fatalf("unexpected file info: %#v", infoStructured)
	}

	searchResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "start_search", Arguments: map[string]any{
			"path": filepath.Dir(path), "pattern": "BETA", "searchType": "content", "filePattern": "*.txt",
			"literalSearch": true, "contextLines": 0,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertToolOK(t, searchResult)
	searchStructured := requireStructuredMap(t, searchResult)
	searchID, _ := searchStructured["sessionId"].(string)
	if searchID == "" {
		t.Fatalf("start_search returned no sessionId: %#v", searchStructured)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		more, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "get_more_search_results", Arguments: map[string]any{"sessionId": searchID, "offset": 0, "length": 100},
		})
		if err != nil {
			t.Fatal(err)
		}
		assertToolOK(t, more)
		structured := requireStructuredMap(t, more)
		if structured["isComplete"] == true {
			if structured["totalMatches"] != float64(1) {
				t.Fatalf("unexpected search result: %#v", structured)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("search did not complete: %#v", structured)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuthEnabledAcceptsCanonicalPublicHostBehindLoopbackProxy(t *testing.T) {
	cfg := config.Default()
	cfg.Audit.Path = filepath.Join(t.TempDir(), "audit.jsonl")
	cfg.Auth.Enabled = true
	cfg.Auth.ExternalURL = "https://mcp.example"
	cfg.Auth.StateDir = filepath.Join(t.TempDir(), "auth")
	if err := oauthsrv.SetPassword(cfg.Auth.StateDir, []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer application.Shutdown(context.Background())

	httpServer := httptest.NewServer(application.http.Handler)
	defer httpServer.Close()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

	req, err := http.NewRequest(http.MethodPost, httpServer.URL+cfg.Server.MCPPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "mcp.example"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("canonical public Host status = %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), `"securitySchemes"`) {
		t.Fatalf("tools/list response lacks OAuth securitySchemes: %s", data)
	}

	req, err = http.NewRequest(http.MethodPost, httpServer.URL+cfg.Server.MCPPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "evil.example"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err = httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-canonical Host status = %d, want 403", resp.StatusCode)
	}
}

type canonicalHostRoundTripper struct {
	base http.RoundTripper
	host string
}

func (r canonicalHostRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Host = r.host
	return r.base.RoundTrip(clone)
}

func assertToolOK(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	if result == nil || result.IsError {
		t.Fatalf("tool returned error: %#v", result)
	}
}

func requireStructuredMap(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured output type = %T", result.StructuredContent)
	}
	return structured
}

func TestNewFailsWhenProcessManagerConfigurationIsInvalid(t *testing.T) {
	cfg := config.Default()
	cfg.Audit.Path = filepath.Join(t.TempDir(), "audit.jsonl")
	cfg.Process.DefaultShell = ""
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(cfg, logger); err == nil {
		t.Fatal("New accepted invalid process manager configuration")
	}
}

func TestNewFailsWhenFilesystemManagerConfigurationIsInvalid(t *testing.T) {
	cfg := config.Default()
	cfg.Audit.Path = filepath.Join(t.TempDir(), "audit.jsonl")
	cfg.Files.DefaultReadLines = -1
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(cfg, logger); err == nil {
		t.Fatal("New accepted invalid filesystem manager configuration")
	}
}

func TestAuthEnabledToolCallChallengesBeforeOSExecution(t *testing.T) {
	cfg := config.Default()
	cfg.Audit.Path = filepath.Join(t.TempDir(), "audit.jsonl")
	cfg.Auth.Enabled = true
	cfg.Auth.ExternalURL = "https://mcp.example"
	cfg.Auth.StateDir = filepath.Join(t.TempDir(), "auth")
	if err := oauthsrv.SetPassword(cfg.Auth.StateDir, []byte("correct horse battery staple")); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer application.searches.Close()
	defer application.processes.Close()
	defer application.audit.Close()

	httpServer := httptest.NewServer(application.http.Handler)
	defer httpServer.Close()
	httpClient := httpServer.Client()
	httpClient.Transport = canonicalHostRoundTripper{base: httpClient.Transport, host: "mcp.example"}

	resp, err := httpClient.Get(httpServer.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var metadata map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["resource"] != "https://mcp.example/mcp" {
		t.Fatalf("protected resource = %#v", metadata["resource"])
	}

	resp, err = httpClient.Get(httpServer.URL + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("OpenID compatibility discovery status = %d, want 200", resp.StatusCode)
	}
	metadata = nil
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["issuer"] != "https://mcp.example" {
		t.Fatalf("OpenID compatibility discovery issuer = %#v", metadata["issuer"])
	}
	scopes, ok := metadata["scopes_supported"].([]any)
	if !ok {
		t.Fatalf("OpenID compatibility discovery scopes = %#v", metadata["scopes_supported"])
	}
	for _, scope := range scopes {
		if scope == "openid" {
			t.Fatal("compatibility discovery must not advertise unsupported OpenID Connect scope")
		}
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "mcpd-auth-test", Version: "dev"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + cfg.Server.MCPPath, HTTPClient: httpClient, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	marker := filepath.Join(t.TempDir(), "must-not-exist")
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "start_process", Arguments: map[string]any{"command": "touch " + marker, "timeout_ms": 1000, "pty": "never"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(fmt.Sprint(result.Meta["mcp/www_authenticate"]), "mcp:write") {
		t.Fatalf("missing tool-level OAuth challenge: %#v", result)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("protected process executed without OAuth; stat err=%v", err)
	}
}

func TestAccessLogIncludesStatusHostAndResponseBytes(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	handler := accessLog(logger, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "blocked", http.StatusForbidden)
	}))
	req := httptest.NewRequest(http.MethodPost, "http://mcp.example/mcp", nil)
	req.Host = "mcp.example"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	text := logs.String()
	for _, want := range []string{`"status":403`, `"host":"mcp.example"`, `"response_bytes":8`} {
		if !strings.Contains(text, want) {
			t.Fatalf("access log missing %s: %s", want, text)
		}
	}
}
