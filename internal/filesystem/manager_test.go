package filesystem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(Options{DefaultReadLines: 3, MaxLineBytes: 1 << 20, NestedEntryLimit: 2, MaxRemoteBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestReadTextPaginationAndTail(t *testing.T) {
	m := testManager(t)
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte("zero\none\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := m.Read(context.Background(), ReadRequest{Path: path, Offset: 1, Length: 2})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Lines, "\n") != "one\ntwo" || got.Content != "" || got.ReadFrom != 1 || got.ReadCount != 2 || got.TotalLines != 5 || got.Remaining != 2 {
		t.Fatalf("unexpected paged read: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"content"`) {
		t.Fatalf("local read serialized duplicate content: %s", encoded)
	}
	tail, err := m.Read(context.Background(), ReadRequest{Path: path, Offset: -2})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tail.Lines, "\n") != "three\nfour" || tail.Content != "" || tail.ReadFrom != 3 || tail.ReadCount != 2 || tail.Remaining != 0 {
		t.Fatalf("unexpected tail read: %+v", tail)
	}
}

func TestWriteAppendAndInfo(t *testing.T) {
	m := testManager(t)
	path := filepath.Join(t.TempDir(), "notes.txt")
	if _, err := m.Write(context.Background(), WriteRequest{Path: path, Content: "alpha\nbeta\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Write(context.Background(), WriteRequest{Path: path, Content: "gamma\n", Mode: "append"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha\nbeta\ngamma\n" {
		t.Fatalf("content = %q", data)
	}
	info, err := m.Info(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.LineCount == nil || *info.LineCount != 3 || info.AppendPosition == nil || *info.AppendPosition != 3 || info.FileType != FileTypeText {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestDirectoryCreateListMove(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := m.CreateDirectory(nested); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(nested, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	listing, err := m.ListDirectory(context.Background(), root, 2)
	if err != nil {
		t.Fatal(err)
	}
	var files, warnings int
	for _, entry := range listing.Entries {
		if entry.Depth == 2 && entry.Type == "file" {
			files++
		}
		if entry.Type == "warning" && entry.Hidden == 1 {
			warnings++
		}
	}
	if files != 2 || warnings != 1 {
		t.Fatalf("unexpected nested limiting: %+v", listing.Entries)
	}
	src := filepath.Join(root, "old.txt")
	dst := filepath.Join(root, "new.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Move(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatal(err)
	}
}

func TestReadMultipleIsolatesFailures(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	good := filepath.Join(root, "good.txt")
	if err := os.WriteFile(good, []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := m.ReadMultiple(context.Background(), []string{good, filepath.Join(root, "missing.txt")})
	if len(results) != 2 || results[0].Result == nil || results[0].Error != "" || results[1].Error == "" {
		t.Fatalf("unexpected multi-read result: %+v", results)
	}
	if results[0].Result.Content != "" || strings.Join(results[0].Result.Lines, "") != "ok" {
		t.Fatalf("multi-read duplicated or lost local text: %+v", results[0].Result)
	}
}

func TestReadURLText(t *testing.T) {
	m := testManager(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("remote\ntext\n"))
	}))
	defer server.Close()
	got, err := m.Read(context.Background(), ReadRequest{Path: server.URL, IsURL: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "url" || got.Content != "remote\ntext\n" || len(got.Lines) != 0 || got.TotalLines != 2 {
		t.Fatalf("unexpected URL read: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"lines"`) {
		t.Fatalf("URL read serialized duplicate lines: %s", encoded)
	}
}

func TestUnsupportedStructuredFormat(t *testing.T) {
	m := testManager(t)
	path := filepath.Join(t.TempDir(), "document.pdf")
	if err := os.WriteFile(path, []byte("%PDF-1.7"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := m.Read(context.Background(), ReadRequest{Path: path})
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("error = %v, want ErrUnsupportedFormat", err)
	}
}

func TestEditExactCountAndFuzzySuggestion(t *testing.T) {
	m := testManager(t)
	path := filepath.Join(t.TempDir(), "edit.txt")
	if err := os.WriteFile(path, []byte("hello brave world\nhello brave world\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile applies the process umask when creating a file. Set the mode
	// explicitly so this test verifies that Edit preserves an existing 0640 mode
	// even when the test process runs under a restrictive umask such as 0077.
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	mismatch, err := m.Edit(context.Background(), EditRequest{Path: path, OldString: "hello brave world", NewString: "changed", ExpectedReplacements: 1})
	if err != nil {
		t.Fatal(err)
	}
	if mismatch.Applied || mismatch.Replacements != 2 {
		t.Fatalf("unexpected mismatch: %+v", mismatch)
	}
	applied, err := m.Edit(context.Background(), EditRequest{Path: path, OldString: "hello brave world", NewString: "changed", ExpectedReplacements: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.Replacements != 2 {
		t.Fatalf("unexpected applied result: %+v", applied)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
	if err := os.WriteFile(path, []byte("hello brave world\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	fuzzy, err := m.Edit(context.Background(), EditRequest{Path: path, OldString: "hello brabe world", NewString: "x", ExpectedReplacements: 1})
	if err != nil {
		t.Fatal(err)
	}
	if fuzzy.Applied || fuzzy.Similarity < fuzzyThreshold || fuzzy.ClosestMatch != "hello brave world" || !strings.Contains(fuzzy.Diff, "{-b-}{+v+}") {
		t.Fatalf("unexpected fuzzy result: %+v", fuzzy)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello brave world\n" {
		t.Fatalf("fuzzy edit modified file: %q", data)
	}
}

func TestEditPreservesSymlinkAndHardLinkSemantics(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Edit(context.Background(), EditRequest{Path: link, OldString: "before", NewString: "after", ExpectedReplacements: 1}); err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatal("edit replaced symlink instead of preserving it")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "after\n" {
		t.Fatalf("symlink target content = %q", data)
	}

	hardA := filepath.Join(root, "hard-a.txt")
	hardB := filepath.Join(root, "hard-b.txt")
	if err := os.WriteFile(hardA, []byte("shared\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hardA, hardB); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Edit(context.Background(), EditRequest{Path: hardA, OldString: "shared", NewString: "updated", ExpectedReplacements: 1}); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(hardB)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "updated\n" {
		t.Fatalf("hard-link peer content = %q", data)
	}
}

func TestReadURLRejectsBinaryBody(t *testing.T) {
	m := testManager(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte{'x', 0, 'y'})
	}))
	defer server.Close()
	_, err := m.Read(context.Background(), ReadRequest{Path: server.URL, IsURL: true})
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("error = %v, want ErrUnsupportedFormat", err)
	}
}

func TestNewManagerRejectsNegativeLimits(t *testing.T) {
	if _, err := NewManager(Options{DefaultReadLines: -1}); err == nil {
		t.Fatal("NewManager accepted a negative read limit")
	}
}

func TestEditManyAppliesAllHunksAtomically(t *testing.T) {
	m := testManager(t)
	path := filepath.Join(t.TempDir(), "multi.txt")
	if err := os.WriteFile(path, []byte("alpha beta alpha\ngamma\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	result, err := m.Edit(context.Background(), EditRequest{Path: path, Edits: []TextEdit{
		{OldString: "alpha", NewString: "A", ExpectedReplacements: 2},
		{OldString: "gamma", NewString: "G"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.Replacements != 3 || len(result.Edits) != 2 || !result.Edits[0].Applied || !result.Edits[1].Applied {
		t.Fatalf("unexpected multi-edit result: %+v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "A beta A\nG\n" {
		t.Fatalf("multi-edit content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %o, want 640", info.Mode().Perm())
	}
}

func TestEditManyValidationFailureRollsBack(t *testing.T) {
	m := testManager(t)
	path := filepath.Join(t.TempDir(), "rollback.txt")
	original := "alpha\nbeta\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := m.Edit(context.Background(), EditRequest{Path: path, Edits: []TextEdit{
		{OldString: "alpha", NewString: "A"},
		{OldString: "missing", NewString: "M"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || len(result.Edits) != 2 || result.Edits[1].ClosestMatch == "" {
		t.Fatalf("unexpected rollback result: %+v", result)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("failed batch modified file: %q", data)
	}
}

func TestEditManyRejectsOverlappingHunks(t *testing.T) {
	m := testManager(t)
	path := filepath.Join(t.TempDir(), "overlap.txt")
	original := "abcdef\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := m.Edit(context.Background(), EditRequest{Path: path, Edits: []TextEdit{
		{OldString: "abcd", NewString: "X"},
		{OldString: "cdef", NewString: "Y"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || !strings.Contains(result.Message, "overlap") {
		t.Fatalf("overlap was not rejected: %+v", result)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatalf("overlap modified file: %q", data)
	}
}

func TestEditManyPreservesSymlinkAndHardLinkSemantics(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	target := filepath.Join(root, "target.txt")
	link := filepath.Join(root, "link.txt")
	if err := os.WriteFile(target, []byte("one two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Edit(context.Background(), EditRequest{Path: link, Edits: []TextEdit{{OldString: "one", NewString: "ONE"}, {OldString: "two", NewString: "TWO"}}}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was not preserved: info=%v err=%v", info, err)
	}
	if data, _ := os.ReadFile(target); string(data) != "ONE TWO\n" {
		t.Fatalf("symlink target = %q", data)
	}

	hardA := filepath.Join(root, "hard-a.txt")
	hardB := filepath.Join(root, "hard-b.txt")
	if err := os.WriteFile(hardA, []byte("red blue\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hardA, hardB); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Edit(context.Background(), EditRequest{Path: hardA, Edits: []TextEdit{{OldString: "red", NewString: "RED"}, {OldString: "blue", NewString: "BLUE"}}}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(hardB); string(data) != "RED BLUE\n" {
		t.Fatalf("hard-link peer = %q", data)
	}
}

func TestListDirectoryPruneMetadataKeepsUsefulDotDirs(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	write := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, ".git", "objects", "aa", "blob"))
	write(filepath.Join(root, "node_modules", "pkg", "index.js"))
	write(filepath.Join(root, ".github", "workflows", "ci.yml"))
	write(filepath.Join(root, "src", "main.go"))

	got, err := m.ListDirectoryWithOptions(context.Background(), DirectoryRequest{Path: root, Depth: 4, MaxEntries: 100})
	if err != nil {
		t.Fatal(err)
	}
	joined := make([]string, 0, len(got.Entries))
	for _, entry := range got.Entries {
		joined = append(joined, filepath.ToSlash(entry.Path))
	}
	text := strings.Join(joined, "\n")
	if strings.Contains(text, ".git/objects/aa") || strings.Contains(text, "node_modules/pkg") {
		t.Fatalf("pruned trees were traversed: entries=%v pruned=%v", joined, got.Pruned)
	}
	if !strings.Contains(text, ".github/workflows/ci.yml") || !strings.Contains(text, "src/main.go") {
		t.Fatalf("useful project content was pruned: %v", joined)
	}
	pruned := strings.Join(got.Pruned, "\n")
	if !strings.Contains(pruned, ".git/objects") || !strings.Contains(pruned, "node_modules") {
		t.Fatalf("missing prune metadata: %+v", got)
	}
}

func TestListDirectoryPruneOverrideAndGlobalCap(t *testing.T) {
	m, err := NewManager(Options{DefaultReadLines: 3, MaxLineBytes: 1 << 20, NestedEntryLimit: 100, MaxRemoteBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for i := 0; i < 10; i++ {
		path := filepath.Join(root, "node_modules", "pkg", fmt.Sprintf("%02d.js", i))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := m.ListDirectoryWithOptions(context.Background(), DirectoryRequest{Path: root, Depth: 4, IncludePruned: true, MaxEntries: 5})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Entries) != 5 || got.MaxEntries != 5 {
		t.Fatalf("global cap not enforced: %+v", got)
	}
	if len(got.Pruned) != 0 {
		t.Fatalf("includePruned still reported pruned paths: %+v", got.Pruned)
	}
}

func TestListDirectoryPrunesDeveloperNoiseByDefault(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	for _, path := range []string{
		filepath.Join(root, ".git", "objects", "aa", "blob"),
		filepath.Join(root, "node_modules", "pkg", "index.js"),
		filepath.Join(root, ".github", "workflows", "ci.yml"),
		filepath.Join(root, "src", "main.go"),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := m.ListDirectoryWithOptions(context.Background(), DirectoryRequest{Path: root, Depth: 4, MaxEntries: 100})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, entry := range result.Entries {
		joined += filepath.ToSlash(entry.Path) + "\n"
	}
	if strings.Contains(joined, ".git/objects/aa") || strings.Contains(joined, "node_modules/pkg") {
		t.Fatalf("noise directory was traversed: %s", joined)
	}
	if !strings.Contains(joined, ".github/workflows") || !strings.Contains(joined, "src/main.go") {
		t.Fatalf("useful repository content was hidden: %s", joined)
	}
	if len(result.Pruned) < 2 {
		t.Fatalf("expected prune metadata, got %+v", result)
	}
}

func TestListDirectoryIncludePrunedOverridesDefault(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	path := filepath.Join(root, "node_modules", "pkg", "index.js")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := m.ListDirectoryWithOptions(context.Background(), DirectoryRequest{Path: root, Depth: 4, MaxEntries: 100, IncludePruned: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range result.Entries {
		if filepath.ToSlash(entry.Path) == "node_modules/pkg/index.js" {
			found = true
		}
	}
	if !found || len(result.Pruned) != 0 {
		t.Fatalf("includePruned did not traverse dependency tree: %+v", result)
	}
}

func TestListDirectoryGlobalCap(t *testing.T) {
	m := testManager(t)
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		path := filepath.Join(root, fmt.Sprintf("dir-%02d", i), "file.txt")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := m.ListDirectoryWithOptions(context.Background(), DirectoryRequest{Path: root, Depth: 3, MaxEntries: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 7 || !result.Truncated || result.MaxEntries != 7 {
		t.Fatalf("global cap not enforced: %+v", result)
	}
}
