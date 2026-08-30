//go:build linux

package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func batchTestManager(t *testing.T, maxParallel int) *Manager {
	t.Helper()
	m, err := NewManager(Options{
		DefaultShell: "/bin/bash", DefaultWaitMS: 100, InitialOutputLines: 20,
		OutputBufferBytes: 1 << 20, MaxLineBytes: 1 << 16, CompletedSessions: 20,
		BatchMaxParallel: maxParallel,
	})
	if err != nil {
		t.Fatal(err)
	}
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
