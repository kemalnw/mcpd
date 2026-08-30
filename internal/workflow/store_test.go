package workflow

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

type countingReaderAt struct {
	data  []byte
	bytes int64
	mu    sync.Mutex
}

func (r *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	r.mu.Lock()
	r.bytes += int64(n)
	r.mu.Unlock()
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (r *countingReaderAt) BytesRead() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}

func TestReadLastLinesReadsNearTailNotWholeLargeLog(t *testing.T) {
	line := strings.Repeat("x", 100) + "\n"
	data := []byte(strings.Repeat(line, 160000)) // ~16 MiB
	reader := &countingReaderAt{data: data}
	lines, err := readLastLinesAt(reader, int64(len(data)), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 100 {
		t.Fatalf("tail lines=%d want 100", len(lines))
	}
	if got := reader.BytesRead(); got > 2*workflowTailBlockBytes {
		t.Fatalf("tail scanned %d bytes for a %d-byte log; expected near-tail bounded reads", got, len(data))
	}
}

func TestReadLastLinesRejectsOversizedSingleLineWithinBudget(t *testing.T) {
	data := []byte(strings.Repeat("x", workflowTailMaxBytes+workflowTailBlockBytes))
	reader := &countingReaderAt{data: data}
	if _, err := readLastLinesAt(reader, int64(len(data)), 10); err == nil || !strings.Contains(err.Error(), "scan budget") {
		t.Fatalf("oversized no-newline log should fail explicitly, got %v", err)
	}
	if got := reader.BytesRead(); got > workflowTailMaxBytes {
		t.Fatalf("oversized tail exceeded scan budget: %d", got)
	}
}

func TestBlockedLogTailDoesNotBlockUnrelatedRunCheckpoint(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runA, err := store.Create(CreateRequest{Title: "a"})
	if err != nil {
		t.Fatal(err)
	}
	runB, err := store.Create(CreateRequest{Title: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendJobLog(runA.ID, "job-a", []byte("one\ntwo\n")); err != nil {
		t.Fatal(err)
	}

	logLock, releaseLogLock := store.logLocks.Acquire(runA.ID + "/job-a")
	logLock.Lock()
	tailDone := make(chan error, 1)
	go func() {
		_, err := store.ReadJobLogTail(runA.ID, "job-a", 1)
		tailDone <- err
	}()
	// Give the tail reader a chance to block on run A's log lock.
	time.Sleep(10 * time.Millisecond)

	checkpointDone := make(chan error, 1)
	go func() {
		_, err := store.Update(runB.ID, runB.Revision, func(r *Run) error { r.Phase = "checkpointed"; return nil })
		checkpointDone <- err
	}()
	select {
	case err := <-checkpointDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("unrelated run checkpoint blocked behind another run's log tail")
	}
	logLock.Unlock()
	releaseLogLock()
	select {
	case err := <-tailDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tail reader did not finish after log lock release")
	}
}

func TestKeyedLockTableDoesNotGrowWithoutBound(t *testing.T) {
	table := newKeyedRWLockTable()
	for i := 0; i < 10000; i++ {
		lock, release := table.Acquire(fmt.Sprintf("key-%d", i))
		lock.Lock()
		lock.Unlock()
		release()
	}
	if got := table.Len(); got != 0 {
		t.Fatalf("keyed lock registry retained %d unused entries", got)
	}
}

func TestConcurrentAppendTailAndUnrelatedCheckpoint(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logsRun, err := store.Create(CreateRequest{Title: "logs"})
	if err != nil {
		t.Fatal(err)
	}
	checkpointRun, err := store.Create(CreateRequest{Title: "checkpoints"})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			if err := store.AppendJobLog(logsRun.ID, "job", []byte(fmt.Sprintf("line-%03d\n", i))); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			if _, err := store.ReadJobLogTail(logsRun.ID, "job", 10); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		rev := checkpointRun.Revision
		for i := 0; i < 20; i++ {
			updated, err := store.Update(checkpointRun.ID, rev, func(r *Run) error { r.Phase = fmt.Sprintf("phase-%d", i); return nil })
			if err != nil {
				errs <- err
				return
			}
			rev = updated.Revision
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	tail, err := store.ReadJobLogTail(logsRun.ID, "job", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 30 || tail[0] != "line-000" || tail[29] != "line-029" {
		t.Fatalf("concurrent log tail corrupted: len=%d tail=%v", len(tail), tail)
	}
}

func TestKeyedLockDoubleReleaseIsSafeAndObservable(t *testing.T) {
	table := newKeyedRWLockTable()
	lock, release := table.Acquire("x")
	lock.Lock()
	lock.Unlock()
	release()
	release()
	if got := table.Len(); got != 0 {
		t.Fatalf("double release leaked lock entry: %d", got)
	}
	if got := table.InvariantViolations(); got != 1 {
		t.Fatalf("double release violations=%d want 1", got)
	}
}

func TestStoreJobLogOperationsBalanceKeyedLockPins(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(CreateRequest{Title: "run"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if err := store.AppendJobLog(run.ID, "job", []byte("line\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReadJobLogTail(run.ID, "job", 2); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.runLocks.InvariantViolations(); got != 0 {
		t.Fatalf("run-lock invariant violations=%d", got)
	}
	if got := store.logLocks.InvariantViolations(); got != 0 {
		t.Fatalf("log-lock invariant violations=%d", got)
	}
	if store.runLocks.Len() != 0 || store.logLocks.Len() != 0 {
		t.Fatalf("keyed locks leaked: run=%d log=%d", store.runLocks.Len(), store.logLocks.Len())
	}
}
