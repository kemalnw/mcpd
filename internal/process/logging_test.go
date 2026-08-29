package process

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type lockedLogBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestProcessLifecycleLoggingDistinguishesToolWaitFromProcessLifetime(t *testing.T) {
	var logs lockedLogBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	m, err := NewManager(Options{DefaultShell: "/bin/bash", DefaultWaitMS: 10, OutputBufferBytes: 1 << 20, MaxLineBytes: 1 << 16, CompletedSessions: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	result, err := m.Start(context.Background(), StartRequest{Command: "sleep 0.15", TimeoutMS: 5, PTY: PTYNever})
	if err != nil {
		t.Fatal(err)
	}
	if result.State == StateExited {
		t.Fatalf("process exited inside short tool wait: %#v", result)
	}

	initial := lifecycleLogEntries(t, logs.String())
	if !hasLifecycleMessage(initial, "process started", result.PID) {
		t.Fatalf("missing process started log: %s", logs.String())
	}
	if hasLifecycleMessage(initial, "process exited", result.PID) {
		t.Fatalf("process incorrectly logged as exited when Start returned: %s", logs.String())
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entries := lifecycleLogEntries(t, logs.String())
		if hasLifecycleMessage(entries, "process exited", result.PID) {
			for _, entry := range entries {
				if entry["msg"] == "process exited" && entry["pid"] == float64(result.PID) {
					if entry["exit_code"] != float64(0) || entry["process_state"] != "exited" {
						t.Fatalf("unexpected exit log: %#v", entry)
					}
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for process exit log: %s", logs.String())
}

func lifecycleLogEntries(t *testing.T, raw string) []map[string]any {
	t.Helper()
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	lines := strings.Split(raw, "\n")
	out := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("decode lifecycle log %q: %v", line, err)
		}
		out = append(out, entry)
	}
	return out
}

func hasLifecycleMessage(entries []map[string]any, message string, pid int) bool {
	for _, entry := range entries {
		if entry["msg"] == message && entry["pid"] == float64(pid) {
			return true
		}
	}
	return false
}
