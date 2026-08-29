//go:build linux

package process

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(Options{
		DefaultShell: "/bin/bash", DefaultWaitMS: 250,
		OutputBufferBytes: 1 << 20, MaxLineBytes: 1 << 16, CompletedSessions: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestStartReturnsCompletedProcess(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{
		Command: "printf 'alpha\\nbeta\\n'", TimeoutMS: 1000, PTY: PTYNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != StateExited || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got := strings.Join(result.Output, "|"); got != "alpha|beta" {
		t.Fatalf("output = %q", got)
	}
}

func TestWaitTimeoutDoesNotTerminateProcess(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{
		Command: "printf 'ready\\n'; sleep 10", TimeoutMS: 30, PTY: PTYNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State == StateExited {
		t.Fatalf("process unexpectedly exited: %+v", result)
	}
	if err := m.ForceTerminate(result.PID); err != nil {
		t.Fatal(err)
	}
}

func TestReadOutputPaginationAndCursor(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{
		Command: "printf '0\\n1\\n2\\n3\\n'", TimeoutMS: 1000, PTY: PTYNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: 0, Length: 2, TimeoutMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(first.Lines, ","); got != "0,1" || first.Remaining != 2 {
		t.Fatalf("unexpected first read: %+v", first)
	}
	second, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: 0, Length: 10, TimeoutMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(second.Lines, ","); got != "2,3" {
		t.Fatalf("unexpected second read: %+v", second)
	}
	tail, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: -2, Length: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tail.Lines, ","); got != "2" {
		t.Fatalf("unexpected tail read: %+v", tail)
	}
}

func TestInteractivePTY(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{
		Command: "python3 -q -i", TimeoutMS: 1500, PTY: PTYAlways,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.PTY {
		t.Fatal("expected PTY session")
	}
	interaction, err := m.Interact(context.Background(), InteractRequest{
		PID: result.PID, Input: "print(6 * 7)", TimeoutMS: 1500, WaitForPrompt: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(interaction.Lines, "\n")
	if !strings.Contains(joined, "42") {
		t.Fatalf("expected REPL output, got %+v", interaction)
	}
	if !interaction.WaitingForInput {
		t.Fatalf("expected prompt detection, got %+v", interaction)
	}
}

func TestUnknownSession(t *testing.T) {
	m := testManager(t)
	_, err := m.ReadOutput(context.Background(), OutputRequest{PID: 999999, TimeoutMS: 1})
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestCompletedSessionRetention(t *testing.T) {
	m, err := NewManager(Options{
		DefaultShell: "/bin/bash", DefaultWaitMS: 100,
		OutputBufferBytes: 1 << 20, MaxLineBytes: 1 << 16, CompletedSessions: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	one, err := m.Start(context.Background(), StartRequest{Command: "true", TimeoutMS: 1000, PTY: PTYNever})
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.Start(context.Background(), StartRequest{Command: "true", TimeoutMS: 1000, PTY: PTYNever})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := m.get(one.PID); errors.Is(err, ErrSessionNotFound) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := m.get(one.PID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("old completed session should be evicted, got %v", err)
	}
	if _, err := m.get(two.PID); err != nil {
		t.Fatalf("new completed session should be retained: %v", err)
	}
}
