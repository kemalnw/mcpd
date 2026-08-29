package filesystem

import (
	"context"
	"errors"
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
	if got.Content != "one\ntwo" || got.ReadFrom != 1 || got.ReadCount != 2 || got.TotalLines != 5 || got.Remaining != 2 {
		t.Fatalf("unexpected paged read: %+v", got)
	}
	tail, err := m.Read(context.Background(), ReadRequest{Path: path, Offset: -2})
	if err != nil {
		t.Fatal(err)
	}
	if tail.Content != "three\nfour" || tail.ReadFrom != 3 || tail.ReadCount != 2 || tail.Remaining != 0 {
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
	if got.Source != "url" || got.Content != "remote\ntext\n" || got.TotalLines != 2 {
		t.Fatalf("unexpected URL read: %+v", got)
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
