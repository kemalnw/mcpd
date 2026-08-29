package search

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeFileSearchHiddenAndTailPagination(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "src", "alpha.go"), "package alpha\n")
	writeTestFile(t, filepath.Join(root, "src", "beta.go"), "package beta\n")
	writeTestFile(t, filepath.Join(root, ".hidden", "alpha.go"), "hidden\n")

	m := newNativeTestManager(t)
	result, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "*.go", SearchType: TypeFiles, IgnoreCase: true, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	read := waitSearchComplete(t, m, result.SessionID)
	if read.TotalMatches != 2 {
		t.Fatalf("matches = %d, want 2: %#v", read.TotalMatches, read.Results)
	}
	tail, err := m.Read(result.SessionID, -1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if tail.ReturnedCount != 1 || tail.HasMoreResults {
		t.Fatalf("tail = %#v", tail)
	}

	hidden, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "alpha.go", SearchType: TypeFiles, IgnoreCase: true, IncludeHidden: true, MaxResults: 10, EarlyTermination: false})
	if err != nil {
		t.Fatal(err)
	}
	hiddenRead := waitSearchComplete(t, m, hidden.SessionID)
	if hiddenRead.TotalMatches != 2 {
		t.Fatalf("hidden matches = %d, want 2: %#v", hiddenRead.TotalMatches, hiddenRead.Results)
	}
}

func TestNativeContentSearchLiteralRegexContextAndMaxResults(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "notes.txt"), "before\nNeedle+One\nmiddle\nneedle+two\nafter\n")
	writeTestFile(t, filepath.Join(root, "skip.md"), "needle+three\n")

	m := newNativeTestManager(t)
	literal, err := m.Start(context.Background(), Options{
		RootPath: root, Pattern: "needle+", SearchType: TypeContent, FilePattern: "*.txt", IgnoreCase: true,
		LiteralSearch: true, ContextLines: 1, MaxResults: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	read := waitSearchComplete(t, m, literal.SessionID)
	if read.TotalMatches != 2 {
		t.Fatalf("literal matches = %d, want 2: %#v", read.TotalMatches, read.Results)
	}
	matchLines := map[int]bool{}
	for _, r := range read.Results {
		if !r.IsContext {
			matchLines[r.Line] = true
		}
	}
	if !matchLines[2] || !matchLines[4] {
		t.Fatalf("overlapping context lost a match: %#v", read.Results)
	}

	regex, err := m.Start(context.Background(), Options{
		RootPath: root, Pattern: `needle\+\w+`, SearchType: TypeContent, FilePattern: "*.txt", IgnoreCase: true,
		ContextLines: 0, MaxResults: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	regexRead := waitSearchComplete(t, m, regex.SessionID)
	if regexRead.TotalMatches != 1 {
		t.Fatalf("max results not enforced: %#v", regexRead)
	}
}

func TestInvalidRegexFailsBeforeSessionCreation(t *testing.T) {
	m := newNativeTestManager(t)
	_, err := m.Start(context.Background(), Options{RootPath: t.TempDir(), Pattern: "[", SearchType: TypeContent, IgnoreCase: true})
	if err == nil || !strings.Contains(err.Error(), "compile search pattern") {
		t.Fatalf("error = %v", err)
	}
	if got := len(m.List()); got != 0 {
		t.Fatalf("invalid search created %d sessions", got)
	}
}

func TestStopCancelsSessionAndPreservesIt(t *testing.T) {
	m := newNativeTestManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	s := newSession("search_stop", Options{SearchType: TypeFiles, Pattern: "x", MaxResults: 10}, "native", cancel)
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()

	stopped := m.Stop(s.id)
	if !stopped.Stopped {
		t.Fatalf("stop = %#v", stopped)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("session context was not cancelled")
	}
	if m.get(s.id) == nil {
		t.Fatal("stop deleted session instead of retaining results")
	}
	s.finish(nil, false, true)
}

func TestCleanupRemovesCompletedSessionsAfterLastReadRetention(t *testing.T) {
	m := newNativeTestManager(t)
	_, cancel := context.WithCancel(context.Background())
	s := newSession("search_old", Options{SearchType: TypeFiles, Pattern: "x", MaxResults: 10}, "native", cancel)
	s.finish(nil, false, false)
	s.mu.Lock()
	s.lastReadTime = time.Now().Add(-10 * time.Minute)
	s.mu.Unlock()
	m.mu.Lock()
	m.sessions[s.id] = s
	m.mu.Unlock()
	m.cleanup(time.Now())
	if m.get(s.id) != nil {
		t.Fatal("expired completed search was not cleaned up")
	}
}

func TestRipgrepBackendWhenAvailable(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed")
	}
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "hello needle world\n")
	m, err := NewManager(ManagerOptions{RipgrepPath: rg, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	start, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "needle", SearchType: TypeContent, LiteralSearch: true, IgnoreCase: true})
	if err != nil {
		t.Fatal(err)
	}
	if start.Backend != "ripgrep" {
		t.Fatalf("backend = %q", start.Backend)
	}
	read := waitSearchComplete(t, m, start.SessionID)
	if read.TotalMatches != 1 || len(read.Results) == 0 || read.Results[0].File == "" {
		t.Fatalf("ripgrep result = %#v", read)
	}
}

func newNativeTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(ManagerOptions{DisableRipgrep: true, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func waitSearchComplete(t *testing.T, m *Manager, sessionID string) ReadResult {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		result, err := m.Read(sessionID, 0, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsComplete {
			return result
		}
		if time.Now().After(deadline) {
			t.Fatalf("search %s did not complete: %#v", sessionID, result)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRipgrepFilePatternIsIntersectionNotUnion(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed")
	}
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "alpha.go"), "")
	writeTestFile(t, filepath.Join(root, "beta.go"), "")
	writeTestFile(t, filepath.Join(root, "alpha.md"), "")
	m, err := NewManager(ManagerOptions{RipgrepPath: rg, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	start, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "alpha", SearchType: TypeFiles, FilePattern: "*.go", IgnoreCase: true, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	read := waitSearchComplete(t, m, start.SessionID)
	if read.TotalMatches != 1 || filepath.Base(read.Results[0].File) != "alpha.go" {
		t.Fatalf("filePattern must intersect main pattern, got %#v", read.Results)
	}
}
