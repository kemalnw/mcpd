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

func TestStartCapturesCompletedProcess(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{
		Command: "printf 'alpha\\nbeta\\n'", TimeoutMS: 1000, PTY: PTYNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.Output, "|"); got != "alpha|beta" || result.ReadFrom != 0 || result.ReadCount != 2 || result.TotalLines != 2 || result.Remaining != 0 {
		t.Fatalf("unexpected initial output page: %+v", result)
	}
	waitForSessionExit(t, m, result.PID, 5*time.Second)
	final, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: 0, Length: 10, TimeoutMS: 100})
	if err != nil {
		t.Fatal(err)
	}
	if final.State != StateExited || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("unexpected final result: %+v", final)
	}
	if len(final.Lines) != 0 || final.ReadFrom != 2 {
		t.Fatalf("initial output was repeated by cursor read: %+v", final)
	}
}

func TestWaitTimeoutDoesNotTerminateProcess(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{
		Command: "sleep 10", TimeoutMS: 30, PTY: PTYNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State == StateExited {
		t.Fatalf("process unexpectedly exited: %+v", result)
	}
	if result.ReadCount != 0 || result.TotalLines != 0 || result.Remaining != 0 || len(result.Output) != 0 {
		t.Fatalf("unexpected running-process initial page: %+v", result)
	}
	if err := m.ForceTerminate(result.PID); err != nil {
		t.Fatal(err)
	}
}

func TestStartCapsInitialOutputPage(t *testing.T) {
	m, err := NewManager(Options{
		DefaultShell: "/bin/bash", DefaultWaitMS: 250, InitialOutputLines: 3,
		OutputBufferBytes: 1 << 20, MaxLineBytes: 1 << 16, CompletedSessions: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	result, err := m.Start(context.Background(), StartRequest{
		Command: "printf '0\\n1\\n2\\n3\\n4\\n'", TimeoutMS: 1000, PTY: PTYNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.Output, ","); got != "0,1,2" {
		t.Fatalf("initial output = %q, want first three lines: %+v", got, result)
	}
	if result.ReadFrom != 0 || result.ReadCount != 3 || result.TotalLines != 5 || result.Remaining != 2 || result.EvictedLines != 0 {
		t.Fatalf("unexpected initial pagination metadata: %+v", result)
	}
}

func TestReadOutputPaginationAndCursor(t *testing.T) {
	m, err := NewManager(Options{
		DefaultShell: "/bin/bash", DefaultWaitMS: 250, InitialOutputLines: 1,
		OutputBufferBytes: 1 << 20, MaxLineBytes: 1 << 16, CompletedSessions: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	result, err := m.Start(context.Background(), StartRequest{
		Command: "printf '0\\n1\\n2\\n3\\n'", TimeoutMS: 1000, PTY: PTYNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.Output, ","); got != "0" || result.Remaining != 3 {
		t.Fatalf("unexpected initial page: %+v", result)
	}
	waitForSessionExit(t, m, result.PID, 5*time.Second)

	first, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: 0, Length: 2, TimeoutMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(first.Lines, ","); got != "1,2" || first.Remaining != 1 {
		t.Fatalf("unexpected first cursor read: %+v", first)
	}
	tail, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: -2, Length: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(tail.Lines, ","); got != "2" {
		t.Fatalf("unexpected tail read: %+v", tail)
	}
	absolute, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: 1, Length: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(absolute.Lines, ","); got != "1" {
		t.Fatalf("unexpected absolute read: %+v", absolute)
	}
	second, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: 0, Length: 10, TimeoutMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(second.Lines, ","); got != "3" {
		t.Fatalf("absolute/tail read moved cursor: %+v", second)
	}
}

func TestStartCursorReadsOnlyFutureOutput(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{
		Command: "printf 'initial\\n'; sleep 1.5; printf 'later\\n'", TimeoutMS: 500, PTY: PTYNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State == StateExited || strings.Join(result.Output, ",") != "initial" || result.ReadCount != 1 {
		t.Fatalf("unexpected initial running-process result: %+v", result)
	}
	future, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: 0, Length: 10, TimeoutMS: 3000})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(future.Lines, ","); got != "later" {
		t.Fatalf("cursor returned initial output again or missed future output: %+v", future)
	}
	waitForSessionExit(t, m, result.PID, 5*time.Second)
	final, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: 0, Length: 10, TimeoutMS: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Lines) != 0 || final.State != StateExited || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("unexpected final cursor/state result: %+v", final)
	}
}

func waitForSessionExit(t *testing.T, m *Manager, pid int, timeout time.Duration) {
	t.Helper()
	s, err := m.get(pid)
	if err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.done:
	case <-timer.C:
		t.Fatalf("process %d did not exit within %s; state=%+v", pid, timeout, s.snapshot())
	}
}

func TestPromptLikeHistoricalOutputDoesNotBlockProcess(t *testing.T) {
	for _, mode := range []PTYMode{PTYNever, PTYAlways} {
		t.Run(string(mode), func(t *testing.T) {
			m := testManager(t)
			result, err := m.Start(context.Background(), StartRequest{
				Command: "printf 'build step $ \\nstill running\\n'; sleep 10", TimeoutMS: 500, PTY: mode,
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.WaitingForInput || result.State == StateWaiting {
				t.Fatalf("historical prompt-like output caused waiting state: %+v", result)
			}
			if err := m.ForceTerminate(result.PID); err != nil {
				t.Fatal(err)
			}
		})
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
