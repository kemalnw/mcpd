package search

import (
	"context"
	"fmt"
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
	matchLines := map[int]Result{}
	contextLines := map[int]Result{}
	for _, r := range read.Results {
		if r.IsContext {
			contextLines[r.Line] = r
			continue
		}
		matchLines[r.Line] = r
	}
	if _, ok := matchLines[2]; !ok {
		t.Fatalf("overlapping context lost line 2 match: %#v", read.Results)
	}
	if _, ok := matchLines[4]; !ok {
		t.Fatalf("overlapping context lost line 4 match: %#v", read.Results)
	}
	first := matchLines[2]
	if first.Text != "Needle+One" || first.Match != "Needle+" || first.Column != 1 || first.EndColumn != 8 {
		t.Fatalf("unexpected native literal match context/span: %#v", first)
	}
	if contextLines[3].Text != "middle" || contextLines[3].Match != "" || contextLines[3].Column != 0 || contextLines[3].EndColumn != 0 {
		t.Fatalf("unexpected native context line: %#v", contextLines[3])
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
	if len(regexRead.Results) != 1 || regexRead.Results[0].Text != "Needle+One" || regexRead.Results[0].Match != "Needle+One" || regexRead.Results[0].Column != 1 || regexRead.Results[0].EndColumn != 11 {
		t.Fatalf("unexpected native regex result: %#v", regexRead.Results)
	}
}

func TestNativeContentSearchUnicodeColumns(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "unicode.txt"), "ππ needle café\n")
	m := newNativeTestManager(t)
	start, err := m.Start(context.Background(), Options{
		RootPath: root, Pattern: "needle", SearchType: TypeContent, FilePattern: "*.txt", LiteralSearch: true, MaxResults: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	read := waitSearchComplete(t, m, start.SessionID)
	if len(read.Results) != 1 {
		t.Fatalf("unexpected unicode result count: %#v", read.Results)
	}
	got := read.Results[0]
	if got.Text != "ππ needle café" || got.Match != "needle" || got.Column != 4 || got.EndColumn != 10 {
		t.Fatalf("unicode columns are not rune based: %#v", got)
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
	got := read.Results[0]
	if got.Text != "hello needle world" || got.Match != "needle" || got.Column != 7 || got.EndColumn != 13 {
		t.Fatalf("ripgrep result lacks full line/span: %#v", got)
	}
}

func TestRipgrepUnicodeColumnsWhenAvailable(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("ripgrep not installed")
	}
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "unicode.txt"), "ππ needle café\n")
	m, err := NewManager(ManagerOptions{RipgrepPath: rg, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	start, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "needle", SearchType: TypeContent, LiteralSearch: true, MaxResults: 10})
	if err != nil {
		t.Fatal(err)
	}
	read := waitSearchComplete(t, m, start.SessionID)
	if len(read.Results) != 1 {
		t.Fatalf("unexpected ripgrep unicode result count: %#v", read.Results)
	}
	got := read.Results[0]
	if got.Text != "ππ needle café" || got.Match != "needle" || got.Column != 4 || got.EndColumn != 10 {
		t.Fatalf("ripgrep unicode columns are not rune based: %#v", got)
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

func TestPreferredRootPathHintNarrowsBroadSearch(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeTestFile(t, filepath.Join(src, "alpha", "go.mod"), "module alpha\n")
	writeTestFile(t, filepath.Join(src, "mcpd", "go.mod"), "module github.com/kemalnw/mcpd\n")
	m, err := NewManager(ManagerOptions{DisableRipgrep: true, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond, PreferredRoots: []string{src}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	start, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "go.mod", PathHint: "mcpd", SearchType: TypeFiles, IgnoreCase: true, MaxResults: 10, EarlyTermination: true})
	if err != nil {
		t.Fatal(err)
	}
	read := waitSearchComplete(t, m, start.SessionID)
	if read.TotalMatches != 1 || len(read.Results) != 1 || read.Results[0].File != filepath.Join(src, "mcpd", "go.mod") {
		t.Fatalf("pathHint did not narrow to mcpd: %#v", read.Results)
	}
	s := m.get(start.SessionID)
	if s == nil || s.opts.RootPath != filepath.Join(src, "mcpd") {
		t.Fatalf("resolved root = %#v", s)
	}
}

func TestPreferredRootExactFilenameAvoidsNoisyHome(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeTestFile(t, filepath.Join(src, "mcpd", "go.mod"), "module github.com/kemalnw/mcpd\n")
	writeTestFile(t, filepath.Join(root, "go", "pkg", "mod", "dependency", "go.mod"), "module dependency\n")
	m, err := NewManager(ManagerOptions{DisableRipgrep: true, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond, PreferredRoots: []string{src}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	start, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "go.mod", SearchType: TypeFiles, IgnoreCase: true, MaxResults: 10, EarlyTermination: true})
	if err != nil {
		t.Fatal(err)
	}
	read := waitSearchComplete(t, m, start.SessionID)
	if read.TotalMatches != 1 || len(read.Results) != 1 || read.Results[0].File != filepath.Join(src, "mcpd", "go.mod") {
		t.Fatalf("preferred root did not beat noisy home dependency: %#v", read.Results)
	}
}

func TestWorkspaceIndexCachesPathHintResolution(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeTestFile(t, filepath.Join(src, "mcpd", "go.mod"), "module mcpd\n")
	m, err := NewManager(ManagerOptions{
		DisableRipgrep: true, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond,
		PreferredRoots: []string{src}, WorkspaceIndexTTL: time.Minute, WorkspaceIndexMaxEntries: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	initialScans := m.workspaceIndexScans
	for i := 0; i < 3; i++ {
		start, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "go.mod", PathHint: "mcpd", SearchType: TypeFiles, IgnoreCase: true, MaxResults: 10, EarlyTermination: true})
		if err != nil {
			t.Fatal(err)
		}
		read := waitSearchComplete(t, m, start.SessionID)
		if read.TotalMatches != 1 || read.Results[0].File != filepath.Join(src, "mcpd", "go.mod") {
			t.Fatalf("cached pathHint resolution failed: %#v", read.Results)
		}
	}
	if m.workspaceIndexScans != initialScans {
		t.Fatalf("repeated pathHint caused index rescan: before=%d after=%d", initialScans, m.workspaceIndexScans)
	}
}

func TestWorkspaceIndexExactNameWinsAndRefreshesStaleEntries(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	exact := filepath.Join(src, "app")
	substring := filepath.Join(src, "app-server")
	writeTestFile(t, filepath.Join(exact, "go.mod"), "module app\n")
	writeTestFile(t, filepath.Join(substring, "go.mod"), "module app-server\n")
	m, err := NewManager(ManagerOptions{
		DisableRipgrep: true, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond,
		PreferredRoots: []string{src}, WorkspaceIndexTTL: 20 * time.Millisecond, WorkspaceIndexMaxEntries: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })

	start, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "go.mod", PathHint: "app", SearchType: TypeFiles, IgnoreCase: true, MaxResults: 10, EarlyTermination: true})
	if err != nil {
		t.Fatal(err)
	}
	read := waitSearchComplete(t, m, start.SessionID)
	if len(read.Results) != 1 || read.Results[0].File != filepath.Join(exact, "go.mod") {
		t.Fatalf("exact repo name did not beat substring collision: %#v", read.Results)
	}

	if err := os.RemoveAll(exact); err != nil {
		t.Fatal(err)
	}
	newRepo := filepath.Join(src, "fresh")
	writeTestFile(t, filepath.Join(newRepo, "go.mod"), "module fresh\n")
	time.Sleep(30 * time.Millisecond)
	fresh, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "go.mod", PathHint: "fresh", SearchType: TypeFiles, IgnoreCase: true, MaxResults: 10, EarlyTermination: true})
	if err != nil {
		t.Fatal(err)
	}
	freshRead := waitSearchComplete(t, m, fresh.SessionID)
	if len(freshRead.Results) != 1 || freshRead.Results[0].File != filepath.Join(newRepo, "go.mod") {
		t.Fatalf("new repo not visible after index TTL refresh: %#v", freshRead.Results)
	}
	m.ensureWorkspaceIndex(time.Now())
	for _, entry := range m.workspaceEntries {
		if entry.Path == exact {
			t.Fatalf("deleted repo remained in refreshed index: %#v", m.workspaceEntries)
		}
	}
}

func TestWorkspaceIndexIsBoundedAndSkipsDependencyTrees(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	for i := 0; i < 8; i++ {
		writeTestFile(t, filepath.Join(src, fmt.Sprintf("repo-%02d", i), "go.mod"), "module x\n")
	}
	writeTestFile(t, filepath.Join(src, "node_modules", "fake-repo", "go.mod"), "module fake\n")
	entries := buildWorkspaceIndex([]string{src}, 3)
	if len(entries) != 3 {
		t.Fatalf("workspace index cap not enforced: %#v", entries)
	}
	for _, entry := range entries {
		if strings.Contains(filepath.ToSlash(entry.Path), "/node_modules/") {
			t.Fatalf("dependency tree was indexed: %#v", entries)
		}
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

func TestWorkspaceIndexAvoidsRepeatedHintWalks(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeTestFile(t, filepath.Join(src, "mcpd", "go.mod"), "module mcpd\n")
	m, err := NewManager(ManagerOptions{DisableRipgrep: true, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond, PreferredRoots: []string{src}, WorkspaceIndexTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	initialScans := m.workspaceIndexScans
	for i := 0; i < 2; i++ {
		start, err := m.Start(context.Background(), Options{RootPath: root, Pattern: "go.mod", PathHint: "mcpd", SearchType: TypeFiles, IgnoreCase: true, MaxResults: 10, EarlyTermination: true})
		if err != nil {
			t.Fatal(err)
		}
		read := waitSearchComplete(t, m, start.SessionID)
		if len(read.Results) != 1 || read.Results[0].File != filepath.Join(src, "mcpd", "go.mod") {
			t.Fatalf("unexpected indexed lookup: %#v", read.Results)
		}
	}
	if m.workspaceIndexScans != initialScans {
		t.Fatalf("repeated lookup refreshed index: scans=%d initial=%d", m.workspaceIndexScans, initialScans)
	}
}

func TestWorkspaceIndexExactNameCollisionIsDeterministic(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	direct := filepath.Join(src, "mcpd")
	nested := filepath.Join(src, "team", "mcpd")
	writeTestFile(t, filepath.Join(direct, "go.mod"), "module direct\n")
	writeTestFile(t, filepath.Join(nested, "go.mod"), "module nested\n")
	m, err := NewManager(ManagerOptions{DisableRipgrep: true, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond, PreferredRoots: []string{src}, WorkspaceIndexTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if got := m.resolveWorkspaceHint(root, "mcpd"); got != direct {
		t.Fatalf("collision resolved to %q, want %q", got, direct)
	}
}

func TestWorkspaceIndexRefreshFindsNewRepository(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(ManagerOptions{DisableRipgrep: true, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond, PreferredRoots: []string{src}, WorkspaceIndexTTL: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	writeTestFile(t, filepath.Join(src, "newrepo", "go.mod"), "module newrepo\n")
	time.Sleep(10 * time.Millisecond)
	if got := m.resolveWorkspaceHint(root, "newrepo"); got != filepath.Join(src, "newrepo") {
		t.Fatalf("refreshed index resolved %q", got)
	}
	if m.workspaceIndexScans < 2 {
		t.Fatalf("expected refresh scan, got %d", m.workspaceIndexScans)
	}
}

func TestWorkspaceIndexDoesNotReturnDeletedRepository(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	repo := filepath.Join(src, "gone")
	writeTestFile(t, filepath.Join(repo, "go.mod"), "module gone\n")
	m, err := NewManager(ManagerOptions{DisableRipgrep: true, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond, PreferredRoots: []string{src}, WorkspaceIndexTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	if got := m.resolveWorkspaceHint(root, "gone"); got != "" {
		t.Fatalf("deleted repository resolved to stale path %q", got)
	}
	if m.workspaceIndexScans < 2 {
		t.Fatalf("stale entry did not trigger refresh: scans=%d", m.workspaceIndexScans)
	}
}

func TestWorkspaceIndexIsBounded(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	for _, name := range []string{"one", "two", "three"} {
		writeTestFile(t, filepath.Join(src, name, "go.mod"), "module "+name+"\n")
	}
	m, err := NewManager(ManagerOptions{DisableRipgrep: true, DefaultMaxResults: 100, Retention: time.Minute, InitialWait: time.Millisecond, PreferredRoots: []string{src}, WorkspaceIndexTTL: time.Hour, WorkspaceIndexMaxEntries: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	if len(m.workspaceEntries) != 2 {
		t.Fatalf("workspace index size = %d, want 2", len(m.workspaceEntries))
	}
}
