package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStartProcessIdempotencyRetryDoesNotExecuteTwice(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	count := filepath.Join(root, "count")
	cmd := "printf 'x\\n' >> " + shellQuote(count) + "; sleep .15; printf 'done\\n'"
	first, err := m.Start(context.Background(), StartRequest{Command: cmd, TimeoutMS: 20, PTY: PTYNever, IdempotencyKey: "lost-start-response"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Start(context.Background(), StartRequest{Command: cmd, TimeoutMS: 1000, PTY: PTYNever, IdempotencyKey: "lost-start-response"})
	if err != nil {
		t.Fatal(err)
	}
	if first.PID != second.PID || !second.IdempotentReplay {
		t.Fatalf("retry created a different process: first=%+v second=%+v", first, second)
	}
	data := waitForFileFields(t, count, 1)
	if got := len(strings.Fields(string(data))); got != 1 {
		t.Fatalf("command executed %d times", got)
	}
}

func TestStartProcessIdempotencyConcurrentRetriesCreateOneProcess(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	count := filepath.Join(root, "count")
	cmd := "printf 'x\\n' >> " + shellQuote(count) + "; sleep .2"
	const callers = 20
	results := make([]StartResult, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = m.Start(context.Background(), StartRequest{Command: cmd, TimeoutMS: 10 + i, PTY: PTYNever, IdempotencyKey: "concurrent-start"})
		}(i)
	}
	wg.Wait()
	pid := results[0].PID
	for i := range results {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if results[i].PID != pid {
			t.Fatalf("caller %d pid=%d want %d", i, results[i].PID, pid)
		}
	}
	data := waitForFileFields(t, count, 1)
	if got := len(strings.Fields(string(data))); got != 1 {
		t.Fatalf("command executed %d times", got)
	}
}

func TestStartProcessIdempotencyRejectsDifferentExecution(t *testing.T) {
	m := testManager(t)
	_, err := m.Start(context.Background(), StartRequest{Command: "sleep .1", TimeoutMS: 10, PTY: PTYNever, IdempotencyKey: "same"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Start(context.Background(), StartRequest{Command: "printf changed", TimeoutMS: 10, PTY: PTYNever, IdempotencyKey: "same"})
	if !errors.Is(err, ErrProcessIdempotencyConflict) {
		t.Fatalf("conflict error=%v", err)
	}
}

func TestInteractOperationKeyWritesAtMostOnce(t *testing.T) {
	m := testManager(t)
	start, err := m.Start(context.Background(), StartRequest{Command: `while IFS= read -r line; do printf 'got:%s\n' "$line"; done`, TimeoutMS: 20, PTY: PTYNever})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Interact(context.Background(), InteractRequest{PID: start.PID, Input: "hello", TimeoutMS: 20, WaitForPrompt: false, OperationKey: "stdin-op-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Interact(context.Background(), InteractRequest{PID: start.PID, Input: "hello", TimeoutMS: 500, WaitForPrompt: true, OperationKey: "stdin-op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.IdempotentReplay || first.PID != second.PID {
		t.Fatalf("interaction replay=%+v", second)
	}
	if _, err := m.Interact(context.Background(), InteractRequest{PID: start.PID, Input: "different", OperationKey: "stdin-op-1"}); !errors.Is(err, ErrInteractionOperationConflict) {
		t.Fatalf("conflicting operation key error=%v", err)
	}
	deadline := time.Now().Add(time.Second)
	var joined string
	for time.Now().Before(deadline) {
		out, readErr := m.ReadOutput(context.Background(), OutputRequest{PID: start.PID, Offset: -20, Length: 20, TimeoutMS: 10})
		if readErr != nil {
			t.Fatal(readErr)
		}
		joined = strings.Join(out.Lines, "\n")
		if strings.Contains(joined, "got:hello") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := strings.Count(joined, "got:hello"); got != 1 {
		t.Fatalf("stdin side effect count=%d output=%q", got, joined)
	}
}

func waitForFileFields(t *testing.T, path string, want int) []byte {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var data []byte
	var err error
	for time.Now().Before(deadline) {
		data, err = os.ReadFile(path)
		if err == nil && len(strings.Fields(string(data))) >= want {
			return data
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("file %s contained %d fields, want at least %d", path, len(strings.Fields(string(data))), want)
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
