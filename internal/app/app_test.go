//go:build linux

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	}
	for _, tool := range listed.Tools {
		if _, ok := required[tool.Name]; ok {
			required[tool.Name] = true
		}
	}
	for name, found := range required {
		if !found {
			t.Errorf("required tool %q not listed", name)
		}
	}

	processResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "start_process", Arguments: map[string]any{"command": "printf 'mcpd-e2e\\n'", "timeout_ms": 1000, "pty": "never"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertToolOK(t, processResult)
	processStructured := requireStructuredMap(t, processResult)
	if processStructured["state"] != "exited" {
		t.Fatalf("unexpected process output: %#v", processStructured)
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
	if readStructured["content"] != "beta" || readStructured["total_lines"] != float64(2) {
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

	resp, err := httpServer.Client().Get(httpServer.URL + "/.well-known/oauth-protected-resource")
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

	client := mcp.NewClient(&mcp.Implementation{Name: "mcpd-auth-test", Version: "dev"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + cfg.Server.MCPPath, HTTPClient: httpServer.Client(), DisableStandaloneSSE: true,
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
