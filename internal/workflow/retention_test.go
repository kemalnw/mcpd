package workflow

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectGarbageDeletesOnlyOldTerminalRuns(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	old, err := store.Create(CreateRequest{Title: "old"})
	if err != nil {
		t.Fatal(err)
	}
	old, err = store.Update(old.ID, old.Revision, func(run *Run) error { run.State = RunCompleted; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendJobLog(old.ID, "test", []byte("evidence\n")); err != nil {
		t.Fatal(err)
	}
	active, err := store.Create(CreateRequest{Title: "active"})
	if err != nil {
		t.Fatal(err)
	}
	active, err = store.Update(active.ID, active.Revision, func(run *Run) error { run.State = RunRunning; return nil })
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * 24 * time.Hour)

	dry, err := store.CollectGarbage(GCPolicy{CompletedRetention: 30 * 24 * time.Hour, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if dry.EligibleRuns != 1 || dry.DeletedRuns != 0 || len(dry.RunIDs) != 1 || dry.RunIDs[0] != old.ID {
		t.Fatalf("dry run=%+v", dry)
	}
	if _, err := store.Get(old.ID); err != nil {
		t.Fatalf("dry run deleted state: %v", err)
	}

	result, err := store.CollectGarbage(GCPolicy{CompletedRetention: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedRuns != 1 || result.DeletedBytes == 0 {
		t.Fatalf("GC=%+v", result)
	}
	if _, err := store.Get(old.ID); err == nil {
		t.Fatal("old terminal run still exists")
	}
	if _, err := store.Get(active.ID); err != nil {
		t.Fatalf("active run was deleted: %v", err)
	}
}

func TestCollectGarbageProtectsActiveLeaseEvenForTerminalRun(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	run, err := store.Create(CreateRequest{Title: "leased"})
	if err != nil {
		t.Fatal(err)
	}
	run, err = store.Update(run.ID, run.Revision, func(run *Run) error { run.State = RunFailed; return nil })
	if err != nil {
		t.Fatal(err)
	}
	// Make the terminal run old first, then acquire a fresh lease. An agent may
	// legitimately resume an old failed run for cleanup/rollback, and GC must
	// honor that new ownership regardless of run age.
	now = now.Add(31 * 24 * time.Hour)
	lease, err := store.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "deploy", OwnerRunID: run.ID, TTL: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.CollectGarbage(GCPolicy{CompletedRetention: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedRuns != 0 || result.SkippedLeasedRuns != 1 {
		t.Fatalf("leased GC=%+v lease=%+v", result, lease)
	}
	if _, err := store.Get(run.ID); err != nil {
		t.Fatalf("leased run deleted: %v", err)
	}
}

func TestCollectGarbageRemovesIdempotencyBeforeRunDeletion(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	run, _, err := store.CreateIdempotent(CreateRequest{Title: "idempotent"}, "key")
	if err != nil {
		t.Fatal(err)
	}
	run, err = store.Update(run.ID, run.Revision, func(run *Run) error { run.State = RunCanceled; return nil })
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * 24 * time.Hour)
	result, err := store.CollectGarbage(GCPolicy{CompletedRetention: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedRuns != 1 || result.PrunedIdempotencyRecords == 0 {
		t.Fatalf("GC=%+v", result)
	}
	if entries, err := os.ReadDir(filepath.Join(store.root, "idempotency")); err == nil && len(entries) != 0 {
		t.Fatalf("idempotency records remain: %v", entries)
	}
}

func TestCollectGarbageCleansCrashTrash(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	trash := filepath.Join(store.root, ".trash", "run_crash-leftover")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trash, "run.json"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := store.CollectGarbage(GCPolicy{CompletedRetention: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if result.CleanedTrashEntries != 1 {
		t.Fatalf("trash cleanup=%+v", result)
	}
	if _, err := os.Stat(trash); !os.IsNotExist(err) {
		t.Fatalf("trash remains: %v", err)
	}
}
