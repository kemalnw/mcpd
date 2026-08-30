//go:build linux

package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func batchTestManager(t *testing.T, maxParallel int) *Manager {
	t.Helper()
	m, err := NewManager(Options{
		DefaultShell: "/bin/bash", DefaultWaitMS: 100, InitialOutputLines: 20,
		OutputBufferBytes: 1 << 20, MaxLineBytes: 1 << 16, CompletedSessions: 20,
		BatchMaxParallel: maxParallel, BatchGlobalParallel: maxParallel,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.resourceProbe = func() HostResources { return HostResources{CPUs: 8, MemoryAvailableB: 8 << 30} }
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestBatchHonorsConcurrencyLimit(t *testing.T) {
	m := batchTestManager(t, 2)
	root := t.TempDir()
	release := filepath.Join(root, "release")
	command := func(id string) string {
		return "printf '" + id + "\\n' >> started; while [ ! -f release ]; do sleep 0.01; done"
	}
	result, err := m.StartBatch(context.Background(), BatchStartRequest{
		MaxParallel: 2, InitialWaitMS: 10,
		Jobs: []BatchJobRequest{
			{ID: "a", Command: command("a"), CWD: root, PTY: PTYNever},
			{ID: "b", Command: command("b"), CWD: root, PTY: PTYNever},
			{ID: "c", Command: command("c"), CWD: root, PTY: PTYNever},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := waitForBatchState(t, m, result.BatchID, func(r BatchResult) bool {
		return r.Counts.Running == 2 && r.Counts.Queued == 1
	})
	if state.MaxParallel != 2 {
		t.Fatalf("max parallel = %d, want 2", state.MaxParallel)
	}
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	final := waitForBatchState(t, m, result.BatchID, func(r BatchResult) bool { return r.State == BatchCompleted })
	if final.Counts.Completed != 3 || final.Counts.Failed != 0 {
		t.Fatalf("unexpected final counts: %+v", final)
	}
}

func TestBatchDeltaReadDoesNotRepeatUnchangedJobs(t *testing.T) {
	m := batchTestManager(t, 2)
	start, err := m.StartBatch(context.Background(), BatchStartRequest{Jobs: []BatchJobRequest{
		{ID: "a", Command: "printf 'a1\\n'; sleep 0.1; printf 'a2\\n'", PTY: PTYNever},
		{ID: "b", Command: "printf 'b1\\n'; sleep 0.1; printf 'b2\\n'", PTY: PTYNever},
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.State == BatchCompleted })
	// Consume every job's final state/output, then changed-only must be empty.
	if _, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: start.BatchID, OnlyChanged: false, Length: 100}); err != nil {
		t.Fatal(err)
	}
	delta, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: start.BatchID, OnlyChanged: true, Length: 100, TimeoutMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Jobs) != 0 {
		t.Fatalf("unchanged jobs were repeated: %+v", delta.Jobs)
	}
}

func TestBatchFailureDoesNotCancelSibling(t *testing.T) {
	m := batchTestManager(t, 2)
	start, err := m.StartBatch(context.Background(), BatchStartRequest{Jobs: []BatchJobRequest{
		{ID: "ok", Command: "printf 'ok\\n'", PTY: PTYNever},
		{ID: "fail", Command: "printf 'bad\\n' >&2; exit 7", PTY: PTYNever},
	}})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.State == BatchCompleted })
	if final.Counts.Completed != 1 || final.Counts.Failed != 1 || final.Counts.Canceled != 0 {
		t.Fatalf("independent failure affected sibling: %+v", final)
	}
}

func TestCancelBatchStopsRunningAndQueuedJobs(t *testing.T) {
	m := batchTestManager(t, 1)
	start, err := m.StartBatch(context.Background(), BatchStartRequest{MaxParallel: 1, Jobs: []BatchJobRequest{
		{ID: "running", Command: "sleep 30", PTY: PTYNever},
		{ID: "queued", Command: "printf 'should-not-run\\n'", PTY: PTYNever},
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.Counts.Running == 1 && r.Counts.Queued == 1 })
	cancel, err := m.CancelBatch(start.BatchID)
	if err != nil {
		t.Fatal(err)
	}
	if cancel.State != BatchCanceled || cancel.Canceled != 2 {
		t.Fatalf("unexpected cancel result: %+v", cancel)
	}
	final := waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.State == BatchCanceled && r.Counts.Canceled == 2 })
	for _, job := range final.Jobs {
		if job.State != BatchJobCanceled {
			t.Fatalf("job was not canceled: %+v", job)
		}
	}
}

func TestBatchCursorIsIndependentFromProcessCursor(t *testing.T) {
	m := batchTestManager(t, 2)
	start, err := m.StartBatch(context.Background(), BatchStartRequest{Jobs: []BatchJobRequest{
		{ID: "a", Command: "printf 'alpha\\n'", PTY: PTYNever},
		{ID: "b", Command: "printf 'beta\\n'", PTY: PTYNever},
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.State == BatchCompleted })
	all, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: start.BatchID, OnlyChanged: false, Length: 100})
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	for _, job := range all.Jobs {
		if job.ID == "a" {
			pid = job.PID
		}
	}
	if pid == 0 {
		t.Fatalf("batch result lacks PID: %+v", all)
	}
	processRead, err := m.ReadOutput(context.Background(), OutputRequest{PID: pid, Offset: 0, Length: 10, TimeoutMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(processRead.Lines, ",") != "alpha" {
		t.Fatalf("batch polling consumed per-process cursor: %+v", processRead)
	}
}

func TestBatchRejectsInvalidInputsBeforeStarting(t *testing.T) {
	m := batchTestManager(t, 2)
	cases := []BatchStartRequest{
		{Jobs: []BatchJobRequest{{ID: "only", Command: "true"}}},
		{Jobs: []BatchJobRequest{{ID: "x", Command: "true"}, {ID: "x", Command: "true"}}},
		{Jobs: []BatchJobRequest{{ID: "a", Command: "true"}, {ID: "b", Command: "true", PTY: PTYAlways}}},
		{InitialWaitMS: -1, Jobs: []BatchJobRequest{{ID: "a", Command: "true"}, {ID: "b", Command: "true"}}},
	}
	for i, req := range cases {
		if _, err := m.StartBatch(context.Background(), req); err == nil {
			t.Fatalf("case %d unexpectedly succeeded", i)
		}
	}
	if got := len(m.ListSessions()); got != 0 {
		t.Fatalf("invalid batch started processes: %d", got)
	}
}

func waitForBatchState(t *testing.T, m *Manager, batchID string, predicate func(BatchResult) bool) BatchResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last BatchResult
	for time.Now().Before(deadline) {
		result, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: batchID, OnlyChanged: false, Length: 100, TimeoutMS: 10})
		if err != nil {
			t.Fatal(err)
		}
		last = result
		if predicate(result) {
			return result
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("batch %s did not reach expected state: %+v", batchID, last)
	return BatchResult{}
}

func TestBatchDAGSchedulesOnlyReadyJobs(t *testing.T) {
	m := batchTestManager(t, 3)
	root := t.TempDir()
	release := filepath.Join(root, "release")
	wait := "while [ ! -f release ]; do sleep 0.01; done"
	start, err := m.StartBatch(context.Background(), BatchStartRequest{MaxParallel: 3, Jobs: []BatchJobRequest{
		{ID: "a", Command: wait, CWD: root, PTY: PTYNever},
		{ID: "b", Command: wait, CWD: root, PTY: PTYNever},
		{ID: "c", Command: "printf 'after\\n'", CWD: root, PTY: PTYNever, DependsOn: []string{"a", "b"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	state := waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.Counts.Running == 2 && r.Counts.Queued == 1 })
	for _, job := range state.Jobs {
		if job.ID == "c" && job.PID != 0 {
			t.Fatalf("dependent job started early: %+v", job)
		}
	}
	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	final := waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.State == BatchCompleted })
	if final.Counts.Completed != 3 || final.Counts.Blocked != 0 {
		t.Fatalf("DAG final = %+v", final)
	}
}

func TestBatchDAGFailureBlocksOnlyDependents(t *testing.T) {
	m := batchTestManager(t, 3)
	start, err := m.StartBatch(context.Background(), BatchStartRequest{Jobs: []BatchJobRequest{
		{ID: "fails", Command: "exit 2", PTY: PTYNever},
		{ID: "blocked", Command: "printf never", PTY: PTYNever, DependsOn: []string{"fails"}},
		{ID: "independent", Command: "true", PTY: PTYNever},
	}})
	if err != nil {
		t.Fatal(err)
	}
	final := waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.State == BatchCompleted })
	if final.Counts.Failed != 1 || final.Counts.Blocked != 1 || final.Counts.Completed != 1 {
		t.Fatalf("failure propagation = %+v", final)
	}
	for _, job := range final.Jobs {
		if job.ID == "blocked" && (job.State != BatchJobBlocked || job.PID != 0 || !strings.Contains(job.Error, "fails")) {
			t.Fatalf("blocked job = %+v", job)
		}
	}
}

func TestBatchDAGRejectsCyclesAndUnknownDependenciesBeforeStart(t *testing.T) {
	m := batchTestManager(t, 2)
	bad := []BatchStartRequest{
		{Jobs: []BatchJobRequest{{ID: "a", Command: "true", DependsOn: []string{"missing"}}, {ID: "b", Command: "true"}}},
		{Jobs: []BatchJobRequest{{ID: "a", Command: "true", DependsOn: []string{"b"}}, {ID: "b", Command: "true", DependsOn: []string{"a"}}}},
	}
	for i, req := range bad {
		if _, err := m.StartBatch(context.Background(), req); err == nil {
			t.Fatalf("bad DAG %d accepted", i)
		}
	}
	if len(m.ListSessions()) != 0 {
		t.Fatal("invalid DAG started processes")
	}
}

func TestBatchCursorCanBeConsumedIndependentlyByTwoClients(t *testing.T) {
	m := batchTestManager(t, 2)
	root := t.TempDir()
	start, err := m.StartBatch(context.Background(), BatchStartRequest{Jobs: []BatchJobRequest{
		{ID: "a", Command: "printf 'a1\\n'; while [ ! -f release ]; do sleep 0.01; done; printf 'a2\\n'", CWD: root, PTY: PTYNever},
		{ID: "b", Command: "printf 'b1\\n'; while [ ! -f release ]; do sleep 0.01; done; printf 'b2\\n'", CWD: root, PTY: PTYNever},
	}})
	if err != nil {
		t.Fatal(err)
	}
	baseline := waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.Counts.Running == 2 })
	if baseline.Cursor == "" {
		t.Fatal("snapshot did not return a cursor")
	}
	cursorA, cursorB := baseline.Cursor, baseline.Cursor
	if err := os.WriteFile(filepath.Join(root, "release"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Freeze batch state before comparing two reads from the same cursor. A
	// caller-owned cursor is a point-in-time observation token; reads made while
	// the batch is still changing may legitimately observe different later data.
	waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.State == BatchCompleted })

	a, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: start.BatchID, OnlyChanged: true, Cursor: cursorA, Length: 100, TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: start.BatchID, OnlyChanged: true, Cursor: cursorB, Length: 100, TimeoutMS: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Jobs) == 0 || len(b.Jobs) == 0 {
		t.Fatalf("one consumer hid changes from another: a=%+v b=%+v", a, b)
	}
	if a.Cursor == cursorA || b.Cursor == cursorB {
		t.Fatal("consumer cursor did not advance")
	}
	if strings.Join(batchLines(a), ",") != strings.Join(batchLines(b), ",") {
		t.Fatalf("independent consumers observed different deltas: a=%v b=%v", batchLines(a), batchLines(b))
	}
}

func TestBatchCursorContinuesRemainingOutputWithoutNewGeneration(t *testing.T) {
	m := batchTestManager(t, 2)
	cmd := "printf '1\\n2\\n3\\n4\\n5\\n'"
	start, err := m.StartBatch(context.Background(), BatchStartRequest{Jobs: []BatchJobRequest{{ID: "a", Command: cmd, PTY: PTYNever}, {ID: "b", Command: cmd, PTY: PTYNever}}})
	if err != nil {
		t.Fatal(err)
	}
	waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.State == BatchCompleted })
	page1, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: start.BatchID, OnlyChanged: false, Length: 2})
	if err != nil {
		t.Fatal(err)
	}
	page2, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: start.BatchID, OnlyChanged: true, Cursor: page1.Cursor, Length: 2, TimeoutMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Jobs) != 2 {
		t.Fatalf("remaining output was not returned: %+v", page2)
	}
	for _, job := range page2.Jobs {
		if strings.Join(job.Lines, ",") != "3,4" || job.Remaining != 1 {
			t.Fatalf("unexpected continuation page: %+v", job)
		}
	}
}

func TestBatchCursorReportsEvictedHistory(t *testing.T) {
	m, err := NewManager(Options{DefaultShell: "/bin/bash", DefaultWaitMS: 50, InitialOutputLines: 20, OutputBufferBytes: 64, MaxLineBytes: 1 << 16, CompletedSessions: 20, BatchMaxParallel: 2, BatchGlobalParallel: 2})
	if err != nil {
		t.Fatal(err)
	}
	m.resourceProbe = func() HostResources { return HostResources{CPUs: 8, MemoryAvailableB: 8 << 30} }
	t.Cleanup(func() { _ = m.Close() })
	root := t.TempDir()
	cmd := "while [ ! -f release ]; do sleep 0.01; done; for i in $(seq 1 30); do printf 'line-%02d-xxxxxxxx\\n' \"$i\"; done"
	start, err := m.StartBatch(context.Background(), BatchStartRequest{Jobs: []BatchJobRequest{{ID: "a", Command: cmd, CWD: root, PTY: PTYNever}, {ID: "b", Command: cmd, CWD: root, PTY: PTYNever}}})
	if err != nil {
		t.Fatal(err)
	}
	baseline := waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.Counts.Running == 2 })
	if err := os.WriteFile(filepath.Join(root, "release"), []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitForBatchState(t, m, start.BatchID, func(r BatchResult) bool { return r.State == BatchCompleted })
	resumed, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: start.BatchID, OnlyChanged: true, Cursor: baseline.Cursor, Length: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range resumed.Jobs {
		if !job.CursorEvicted || job.EvictedLines == 0 {
			t.Fatalf("evicted cursor was silent: %+v", job)
		}
	}
}

func TestBatchCursorRejectsDifferentBatch(t *testing.T) {
	m := batchTestManager(t, 2)
	one, err := m.StartBatch(context.Background(), BatchStartRequest{Jobs: []BatchJobRequest{{ID: "a", Command: "true"}, {ID: "b", Command: "true"}}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.StartBatch(context.Background(), BatchStartRequest{Jobs: []BatchJobRequest{{ID: "a", Command: "true"}, {ID: "b", Command: "true"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: two.BatchID, Cursor: one.Cursor, OnlyChanged: true}); err == nil {
		t.Fatal("cursor from another batch was accepted")
	}
}

func batchLines(result BatchResult) []string {
	out := []string{}
	for _, job := range result.Jobs {
		out = append(out, job.Lines...)
	}
	return out
}

func TestBatchIdempotencyConcurrentRetriesCreateOneLogicalBatch(t *testing.T) {
	m := batchTestManager(t, 2)
	root := t.TempDir()
	req := BatchStartRequest{IdempotencyKey: "transport-retry-123", Jobs: []BatchJobRequest{
		{ID: "a", Command: "printf 'a\\n' >> started; sleep 0.05", CWD: root, PTY: PTYNever},
		{ID: "b", Command: "printf 'b\\n' >> started; sleep 0.05", CWD: root, PTY: PTYNever},
	}}
	const callers = 16
	var wg sync.WaitGroup
	ids := make(chan string, callers)
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := m.StartBatch(context.Background(), req)
			if err != nil {
				errs <- err
				return
			}
			ids <- result.BatchID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var batchID string
	for id := range ids {
		if batchID == "" {
			batchID = id
		}
		if id != batchID {
			t.Fatalf("duplicate logical batches: %s != %s", id, batchID)
		}
	}
	waitForBatchState(t, m, batchID, func(r BatchResult) bool { return r.State == BatchCompleted })
	data, err := os.ReadFile(filepath.Join(root, "started"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Fields(string(data))); got != 2 {
		t.Fatalf("idempotent retries executed duplicate jobs: lines=%d data=%q", got, data)
	}
}

func TestBatchIdempotencyRetryReturnsSameHandleWithoutConsumingProcessOutput(t *testing.T) {
	m := batchTestManager(t, 2)
	req := BatchStartRequest{IdempotencyKey: "lost-response", Jobs: []BatchJobRequest{
		{ID: "a", Command: "printf 'alpha\\n'", PTY: PTYNever},
		{ID: "b", Command: "printf 'beta\\n'", PTY: PTYNever},
	}}
	first, err := m.StartBatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	waitForBatchState(t, m, first.BatchID, func(r BatchResult) bool { return r.State == BatchCompleted })
	replay, err := m.StartBatch(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if replay.BatchID != first.BatchID || !replay.IdempotentReplay {
		t.Fatalf("retry did not return same logical operation: first=%+v replay=%+v", first, replay)
	}
	var pid int
	for _, job := range replay.Jobs {
		if job.ID == "a" {
			pid = job.PID
		}
	}
	if pid == 0 {
		t.Fatalf("replay lacks managed PID: %+v", replay)
	}
	out, err := m.ReadOutput(context.Background(), OutputRequest{PID: pid, Offset: 0, Length: 10, TimeoutMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(out.Lines, ",") != "alpha" {
		t.Fatalf("idempotency replay consumed per-process output cursor: %+v", out)
	}
}

func TestBatchIdempotencyRejectsConflictingKeyReuse(t *testing.T) {
	m := batchTestManager(t, 2)
	first := BatchStartRequest{IdempotencyKey: "same-key", Jobs: []BatchJobRequest{{ID: "a", Command: "true"}, {ID: "b", Command: "true"}}}
	if _, err := m.StartBatch(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.Jobs = []BatchJobRequest{{ID: "a", Command: "printf changed"}, {ID: "b", Command: "true"}}
	if _, err := m.StartBatch(context.Background(), second); !errors.Is(err, ErrBatchIdempotencyConflict) {
		t.Fatalf("conflicting key reuse error=%v", err)
	}
}
