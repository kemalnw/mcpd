package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCreateIdempotentConcurrentRetriesCreateOneRun(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := CreateRequest{Title: "parallel upgrade", Objective: "finish", SuccessCriteria: []string{"green"}}
	const callers = 16
	var wg sync.WaitGroup
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run, _, err := store.CreateIdempotent(req, "request-123")
			if err != nil {
				errs <- err
				return
			}
			ids <- run.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("duplicate logical runs: %s != %s", id, first)
		}
	}
	runs, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].ID != first {
		t.Fatalf("runs=%+v", runs)
	}
}

func TestCreateIdempotentPersistsAcrossStoreReopen(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	req := CreateRequest{Title: "upgrade", Objective: "same"}
	first, replay, err := store.CreateIdempotent(req, "persist-key")
	if err != nil {
		t.Fatal(err)
	}
	if replay {
		t.Fatal("first create reported replay")
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	second, replay, err := reopened.CreateIdempotent(req, "persist-key")
	if err != nil {
		t.Fatal(err)
	}
	if !replay || second.ID != first.ID {
		t.Fatalf("reopen replay=%v first=%s second=%s", replay, first.ID, second.ID)
	}
}

func TestCreateIdempotentRejectsConflictingReuse(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateIdempotent(CreateRequest{Title: "one"}, "same-key"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateIdempotent(CreateRequest{Title: "two"}, "same-key"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflicting reuse error=%v", err)
	}
}

func TestCreateIdempotentRepairsRecordWithoutRunAfterCrash(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	req := CreateRequest{Title: "recover", Objective: "same"}
	first, _, err := store.CreateIdempotent(req, "crash-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, first.ID, "run.json")); err != nil {
		t.Fatal(err)
	}
	repaired, replay, err := store.CreateIdempotent(req, "crash-key")
	if err != nil {
		t.Fatal(err)
	}
	if !replay || repaired.ID != first.ID {
		t.Fatalf("repair replay=%v repaired=%+v first=%+v", replay, repaired, first)
	}
}

func TestExpiredIdempotencyKeyCanCreateNewRun(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	req := CreateRequest{Title: "run"}
	first, _, err := store.CreateIdempotent(req, "expiring")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(durableIdempotencyTTL + time.Second)
	second, replay, err := store.CreateIdempotent(req, "expiring")
	if err != nil {
		t.Fatal(err)
	}
	if replay || second.ID == first.ID {
		t.Fatalf("expired key was reused: first=%s second=%s replay=%v", first.ID, second.ID, replay)
	}
}

func TestCreateIdempotentDoesNotPersistRawKey(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "customer-visible-secret-key"
	if _, _, err := store.CreateIdempotent(CreateRequest{Title: "run"}, secret); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "idempotency"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() == secret+".json" {
		t.Fatalf("raw key leaked in filename: %+v", entries)
	}
	data, err := os.ReadFile(filepath.Join(root, "idempotency", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || containsBytes(data, []byte(secret)) {
		t.Fatalf("raw key leaked in persisted record: %s", data)
	}
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
