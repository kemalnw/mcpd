package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kemalnw/mcpd/internal/audit"
	processmgr "github.com/kemalnw/mcpd/internal/process"
)

func TestAuditedLogsStartProcessAndCorrelatesAuditEvent(t *testing.T) {
	var logs bytes.Buffer
	withActivityTestLogger(t, &logs)

	store, err := audit.Open(true, filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	exitCode := 0
	handler := audited(store, "start_process", func(context.Context, StartProcessInput) (processmgr.StartResult, error) {
		return processmgr.StartResult{PID: 4242, CWD: "/srv/app", Shell: "/bin/bash", PTY: false, State: processmgr.StateExited, ExitCode: &exitCode, WaitedMS: 7}, nil
	})
	_, _, err = handler(context.Background(), nil, StartProcessInput{Command: "df -h", CWD: "/srv/app", TimeoutMS: 5000, PTY: "never"})
	if err != nil {
		t.Fatal(err)
	}

	entries := decodeLogEntries(t, logs.String())
	if len(entries) != 2 {
		t.Fatalf("log entry count = %d, want 2: %s", len(entries), logs.String())
	}
	call, result := entries[0], entries[1]
	if call["msg"] != "mcp tool call" || call["tool"] != "start_process" || call["command_sha256"] != auditDigest("df -h") || call["command_bytes"] != float64(len("df -h")) || call["cwd"] != "/srv/app" {
		t.Fatalf("unexpected call log: %#v", call)
	}
	if result["msg"] != "mcp tool result" || result["status"] != "success" || result["pid"] != float64(4242) || result["cwd"] != "/srv/app" || result["process_state"] != "exited" || result["exit_code"] != float64(0) {
		t.Fatalf("unexpected result log: %#v", result)
	}
	if call["event_id"] == "" || call["event_id"] != result["event_id"] {
		t.Fatalf("runtime event ids do not correlate: call=%#v result=%#v", call["event_id"], result["event_id"])
	}
	recent := store.Recent(1)
	if len(recent) != 1 || recent[0].ID != call["event_id"] || recent[0].Tool != "start_process" {
		t.Fatalf("audit event does not correlate with runtime log: %#v", recent)
	}
	encodedAudit, err := json.Marshal(recent[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedAudit), "df -h") {
		t.Fatalf("raw command leaked into durable audit: %s", encodedAudit)
	}
}

func TestAuditedLogsErrors(t *testing.T) {
	var logs bytes.Buffer
	withActivityTestLogger(t, &logs)

	handler := audited[PIDInput, TerminateOutput](nil, "kill_process", func(context.Context, PIDInput) (TerminateOutput, error) {
		return TerminateOutput{}, errors.New("permission denied")
	})
	_, _, err := handler(context.Background(), nil, PIDInput{PID: 99})
	if err == nil {
		t.Fatal("handler unexpectedly succeeded")
	}

	entries := decodeLogEntries(t, logs.String())
	if len(entries) != 2 || entries[1]["status"] != "error" || entries[1]["error_sha256"] != auditDigest("permission denied") || entries[1]["tool"] != "kill_process" {
		t.Fatalf("unexpected error logs: %#v", entries)
	}
}

func TestOperationalLogsSanitizeLargeOrSensitiveToolPayloads(t *testing.T) {
	var logs bytes.Buffer
	withActivityTestLogger(t, &logs)

	const writeSecret = "WRITE-CONTENT-MUST-NOT-APPEAR"
	const interactiveSecret = "INTERACTIVE-SECRET-MUST-NOT-APPEAR"
	logToolCall(context.Background(), "evt_write", "write_file", WriteFileInput{Path: "/tmp/example.txt", Content: writeSecret, Mode: "rewrite"})
	logToolCall(context.Background(), "evt_interact", "interact_with_process", InteractWithProcessInput{PID: 7, Input: interactiveSecret + "\n", TimeoutMS: 1000})

	text := logs.String()
	if strings.Contains(text, writeSecret) || strings.Contains(text, interactiveSecret) {
		t.Fatalf("operational logs leaked tool payload: %s", text)
	}
	if !strings.Contains(text, `"path":"/tmp/example.txt"`) || !strings.Contains(text, `"content_bytes":`) || !strings.Contains(text, `"input_bytes":`) {
		t.Fatalf("operational logs are missing safe metadata: %s", text)
	}
}

func TestOperationalLogsIncludeCompactSearchMetadata(t *testing.T) {
	var logs bytes.Buffer
	withActivityTestLogger(t, &logs)

	logToolCall(context.Background(), "evt_search", "start_search", StartSearchInput{
		Path: "/srv/app", Pattern: "TODO", SearchType: "content", FilePattern: "*.go", MaxResults: 25, TimeoutMS: 5000,
	})
	entries := decodeLogEntries(t, logs.String())
	if len(entries) != 1 {
		t.Fatalf("log entry count = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry["tool"] != "start_search" || entry["path"] != "/srv/app" || entry["pattern_bytes"] != float64(len("TODO")) || entry["pattern_sha256"] != auditDigest("TODO") || entry["search_type"] != "content" || entry["file_pattern"] != "*.go" {
		t.Fatalf("unexpected search activity log: %#v", entry)
	}
	if _, leaked := entry["pattern"]; leaked {
		t.Fatalf("raw search pattern leaked: %#v", entry)
	}
}

func TestStartProcessCommandIsHashedNotLogged(t *testing.T) {
	var logs bytes.Buffer
	withActivityTestLogger(t, &logs)

	command := "curl -H 'Authorization: Bearer super-secret-token' https://example.test"
	logToolCall(context.Background(), "evt_secret", "start_process", StartProcessInput{Command: command, TimeoutMS: 1})
	entries := decodeLogEntries(t, logs.String())
	if len(entries) != 1 {
		t.Fatalf("entries=%#v", entries)
	}
	entry := entries[0]
	if entry["command_sha256"] != auditDigest(command) || entry["command_bytes"] != float64(len(command)) {
		t.Fatalf("command metadata missing: %#v", entry)
	}
	if strings.Contains(logs.String(), "super-secret-token") || strings.Contains(logs.String(), command) {
		t.Fatalf("raw command/secret leaked into runtime activity log: %s", logs.String())
	}
}

func TestRemoteReadLogStripsURLCredentialsAndQuery(t *testing.T) {
	var logs bytes.Buffer
	withActivityTestLogger(t, &logs)
	logToolCall(context.Background(), "evt_url", "read_file", ReadFileInput{Path: "https://user:pass@example.test/x?token=secret#fragment", IsURL: true})
	if raw := logs.String(); strings.Contains(raw, "pass") || strings.Contains(raw, "token=secret") || strings.Contains(raw, "fragment") {
		t.Fatalf("URL credentials/query leaked: %s", raw)
	}
}

func withActivityTestLogger(t *testing.T, buf *bytes.Buffer) {
	t.Helper()
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
}

func decodeLogEntries(t *testing.T, raw string) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil
	}
	out := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

func TestDurableCommandAndIdempotencyKeyAreNotLogged(t *testing.T) {
	var logs bytes.Buffer
	withActivityTestLogger(t, &logs)
	command := "curl -H 'Authorization: Bearer durable-super-secret' https://example.test"
	key := "durable-idempotency-secret"
	logToolCall(context.Background(), "evt_durable", "start_durable_job", StartDurableJobInput{Command: command, CWD: "/srv/repo", IdempotencyKey: key})
	text := logs.String()
	if strings.Contains(text, command) || strings.Contains(text, "durable-super-secret") || strings.Contains(text, key) {
		t.Fatalf("durable command/key leaked into runtime log: %s", text)
	}
	entries := decodeLogEntries(t, text)
	if len(entries) != 1 || entries[0]["command_sha256"] != auditDigest(command) || entries[0]["has_idempotency_key"] != true {
		t.Fatalf("durable safe metadata missing: %#v", entries)
	}
}
