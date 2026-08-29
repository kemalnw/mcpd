//go:build linux

package app

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kemalnw/mcpd/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStatelessMCPProcessToolEndToEnd(t *testing.T) {
	cfg := config.Default()
	cfg.Audit.Path = filepath.Join(t.TempDir(), "audit.jsonl")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := New(cfg, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer application.processes.Close()
	defer application.audit.Close()

	httpServer := httptest.NewServer(application.http.Handler)
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "mcpd-test", Version: "dev"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             httpServer.URL + cfg.Server.MCPPath,
		HTTPClient:           httpServer.Client(),
		DisableStandaloneSSE: true,
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
	if len(listed.Tools) != 7 {
		t.Fatalf("tool count = %d, want 7", len(listed.Tools))
	}

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "start_process",
		Arguments: map[string]any{
			"command":    "printf 'mcpd-e2e\\n'",
			"timeout_ms": 1000,
			"pty":        "never",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("tool returned error: %+v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("structured output type = %T", result.StructuredContent)
	}
	if structured["state"] != "exited" {
		t.Fatalf("unexpected structured output: %#v", structured)
	}
}
