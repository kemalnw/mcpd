package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestStoreCreateReloadAndList(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runs")
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(CreateRequest{Title: "parallel upgrade", Objective: "finish safely", SuccessCriteria: []string{"green CI"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.SchemaVersion != SchemaVersion || run.Revision != 1 || run.State != RunPlanned {
		t.Fatalf("unexpected run: %+v", run)
	}

	reloaded, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Get(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != run.Title || got.Objective != run.Objective || got.Revision != 1 {
		t.Fatalf("reloaded run = %+v", got)
	}
	listed, err := reloaded.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != run.ID {
		t.Fatalf("listed = %+v", listed)
	}
}

func TestStoreRevisionedAtomicUpdate(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(CreateRequest{Title: "run"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(run.ID, run.Revision, func(r *Run) error {
		r.State = RunRunning
		r.Phase = "implementation"
		r.Items = []WorkItem{{ID: "a", State: ItemRunning}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.State != RunRunning || len(updated.Items) != 1 {
		t.Fatalf("updated = %+v", updated)
	}
	if _, err := store.Update(run.ID, 1, func(*Run) error { return nil }); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
}

func TestStoreConcurrentRevisionAllowsOneWinner(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(CreateRequest{Title: "run"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.Update(run.ID, run.Revision, func(r *Run) error { r.Phase = "x"; return nil })
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrRevisionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflicts)
	}
}

func TestStoreDetectsCorruptState(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(CreateRequest{Title: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, run.ID, "run.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(run.ID); err == nil || !strings.Contains(err.Error(), "decode workflow run") {
		t.Fatalf("corrupt read error = %v", err)
	}
}

func TestJobLogsPersistAndTail(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(CreateRequest{Title: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendJobLog(run.ID, "job-a", []byte("one\ntwo\n")); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendJobLog(run.ID, "job-a", []byte("three\nfour\n")); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	tail, err := reloaded.ReadJobLogTail(run.ID, "job-a", 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tail, ",") != "three,four" {
		t.Fatalf("tail = %v", tail)
	}
	info, err := os.Stat(filepath.Join(root, run.ID, "logs", "job-a.log"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %o", info.Mode().Perm())
	}
}

func TestUnsafeHandlesRejected(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("../escape"); err == nil {
		t.Fatal("unsafe run id accepted")
	}
}
