//go:build linux

package process

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
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

func TestStartUsesWorkingDirectory(t *testing.T) {
	m := testManager(t)
	cwd := t.TempDir()
	script := filepath.Join(cwd, "hello.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'cwd-ok\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := m.Start(context.Background(), StartRequest{
		Command: "./hello.sh", CWD: cwd, TimeoutMS: 1000, PTY: PTYNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CWD != cwd || strings.Join(result.Output, ",") != "cwd-ok" {
		t.Fatalf("command did not execute from cwd: %+v", result)
	}
	sessions := m.ListSessions()
	if len(sessions) == 0 || sessions[0].CWD != cwd {
		t.Fatalf("session did not retain cwd metadata: %+v", sessions)
	}
}

func TestStartRejectsInvalidWorkingDirectory(t *testing.T) {
	m := testManager(t)
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := m.Start(context.Background(), StartRequest{Command: "true", CWD: missing, TimeoutMS: 1000, PTY: PTYNever}); err == nil || !strings.Contains(err.Error(), "cwd") || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing cwd error = %v", err)
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(context.Background(), StartRequest{Command: "true", CWD: file, TimeoutMS: 1000, PTY: PTYNever}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file cwd error = %v", err)
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
				Command: "printf 'build step $ '; sleep 0.02; printf '\\nstill running\\n'; sleep 10", TimeoutMS: 500, PTY: mode,
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

func TestReadOutputObservesPartialLineGeneration(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{
		Command: "printf 'phase1'; sleep 0.4; printf ' phase2'; sleep 10", TimeoutMS: 150, PTY: PTYNever,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.Output, "") != "phase1" {
		t.Fatalf("unexpected initial partial output: %+v", result)
	}
	update, err := m.ReadOutput(context.Background(), OutputRequest{PID: result.PID, Offset: 0, Length: 10, TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if update.ReadCount != 0 || update.LatestLine == nil || update.LatestLine.Text != "phase1 phase2" || update.Generation == 0 {
		t.Fatalf("partial-line update was not observable: %+v", update)
	}
	if err := m.ForceTerminate(result.PID); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRawInputDoesNotAppendNewline(t *testing.T) {
	var buf bytes.Buffer
	s := &session{stdin: &buf, state: StateRunning, notify: make(chan struct{})}
	if err := s.write("abc", true); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "abc" {
		t.Fatalf("raw input = %q, want exact bytes", got)
	}
	buf.Reset()
	if err := s.write("abc", false); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "abc\n" {
		t.Fatalf("default input = %q, want newline", got)
	}
}

func TestSeparateStreamsOptIn(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{
		Command: "printf 'out\\n'; printf 'err\\n' >&2", TimeoutMS: 1000, PTY: PTYNever, SeparateStreams: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Output) != 0 || len(result.Streams) != 2 {
		t.Fatalf("unexpected separated output: %+v", result)
	}
	seen := map[string]string{}
	for _, line := range result.Streams {
		seen[line.Stream] = line.Text
	}
	if seen["stdout"] != "out" || seen["stderr"] != "err" {
		t.Fatalf("stream identity lost: %+v", result.Streams)
	}
}

func TestResizePTYChangesTerminalSize(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{Command: "sleep 10", TimeoutMS: 50, PTY: PTYAlways})
	if err != nil {
		t.Fatal(err)
	}
	resized, err := m.ResizePTY(result.PID, 55, 132)
	if err != nil {
		t.Fatal(err)
	}
	if resized.Rows != 55 || resized.Cols != 132 {
		t.Fatalf("unexpected resize result: %+v", resized)
	}
	s, err := m.get(result.PID)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	ptmx := s.ptyFile
	if ptmx == nil {
		s.mu.Unlock()
		t.Fatal("PTY closed before size verification")
	}
	rows, cols, err := pty.Getsize(ptmx)
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if rows != 55 || cols != 132 {
		t.Fatalf("PTY size = %dx%d, want 55x132", rows, cols)
	}
	if err := m.ForceTerminate(result.PID); err != nil {
		t.Fatal(err)
	}
}

func TestResizePTYRejectsNonPTYSession(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{Command: "sleep 10", TimeoutMS: 50, PTY: PTYNever})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ResizePTY(result.PID, 24, 80); err == nil || !strings.Contains(err.Error(), "not a PTY") {
		t.Fatalf("non-PTY resize error = %v", err)
	}
	if err := m.ForceTerminate(result.PID); err != nil {
		t.Fatal(err)
	}
}

func TestForceTerminateCompletedSessionNeverSignalsReusedPID(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{Command: "true", TimeoutMS: 1000, PTY: PTYNever})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil {
		t.Fatalf("process did not complete: %+v", result)
	}
	calls := 0
	m.signalGroup = func(pid int, sig syscall.Signal) error {
		calls++
		return errors.New("signal must not be called for completed session")
	}
	if err := m.ForceTerminate(result.PID); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("completed retained PID was signaled %d time(s)", calls)
	}
}

func TestForceTerminateRunningSessionUsesManagedSignalPath(t *testing.T) {
	m := testManager(t)
	result, err := m.Start(context.Background(), StartRequest{Command: "sleep 30", TimeoutMS: 10, PTY: PTYNever})
	if err != nil {
		t.Fatal(err)
	}
	original := m.signalGroup
	calls := 0
	m.signalGroup = func(pid int, sig syscall.Signal) error {
		calls++
		return original(pid, sig)
	}
	if err := m.ForceTerminate(result.PID); err != nil {
		t.Fatal(err)
	}
	if calls == 0 {
		t.Fatal("running managed process was not signaled")
	}
}
