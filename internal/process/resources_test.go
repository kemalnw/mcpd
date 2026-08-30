package process

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecommendedGlobalParallelUsesSafeStaticFallbacks(t *testing.T) {
	cases := []struct {
		name string
		in   HostResources
		want int
	}{
		{name: "normal", in: HostResources{CPUs: 4, MemoryAvailableB: 8 << 30}, want: 4},
		{name: "unknown memory", in: HostResources{CPUs: 6}, want: 6},
		{name: "tiny memory", in: HostResources{CPUs: 8, MemoryAvailableB: 400 << 20}, want: 1},
		{name: "low memory", in: HostResources{CPUs: 8, MemoryAvailableB: 800 << 20}, want: 2},
		{name: "cpu floor", in: HostResources{CPUs: 1, MemoryAvailableB: 4 << 30}, want: 2},
		{name: "cpu ceiling", in: HostResources{CPUs: 64, MemoryAvailableB: 64 << 30}, want: 16},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := recommendedGlobalParallel(tc.in); got != tc.want {
				t.Fatalf("recommendedGlobalParallel(%+v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestResourceWeightAccountsForClassAndPressure(t *testing.T) {
	resources := HostResources{CPUs: 8, MemoryAvailableB: 8 << 30}
	if got := resourceWeight(ResourceIO, resources, 8); got != 1 {
		t.Fatalf("io weight = %d, want 1", got)
	}
	if got := resourceWeight(ResourceCPU, resources, 8); got != 2 {
		t.Fatalf("cpu weight = %d, want 2", got)
	}
	if got := resourceWeight(ResourceHeavy, resources, 8); got != 4 {
		t.Fatalf("heavy weight = %d, want 4", got)
	}
	pressured := HostResources{CPUs: 4, Load1: 7, MemoryAvailableB: 400 << 20}
	if got := resourceWeight(ResourceCPU, pressured, 8); got != 6 {
		t.Fatalf("pressured cpu weight = %d, want 6", got)
	}
	if got := resourceWeight(ResourceHeavy, pressured, 4); got != 4 {
		t.Fatalf("weight should clamp to capacity, got %d", got)
	}
}

func TestWeightedLimiterAcquiresHeavyWeightAtomically(t *testing.T) {
	limiter := newWeightedLimiter(4)
	firstCancel := make(chan struct{})
	if !limiter.Acquire(2, firstCancel) {
		t.Fatal("initial reservation unexpectedly canceled")
	}
	cancel := make(chan struct{})
	done := make(chan bool, 1)
	go func() { done <- limiter.Acquire(4, cancel) }()

	deadline := time.Now().Add(time.Second)
	for limiter.queued() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := limiter.inUse(); got != 2 {
		t.Fatalf("waiting heavy acquire partially reserved capacity: in_use=%d, want 2", got)
	}
	close(cancel)
	select {
	case acquired := <-done:
		if acquired {
			t.Fatal("canceled queued acquire unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled atomic acquire did not unblock")
	}
	if got := limiter.inUse(); got != 2 {
		t.Fatalf("capacity changed after canceled waiter: in_use=%d, want 2", got)
	}
	limiter.Release(2)
}

func TestWeightedLimiterFIFOStopsLightJobStarvingHeavyWaiter(t *testing.T) {
	limiter := newWeightedLimiter(4)
	neverCancel := make(chan struct{})
	if !limiter.Acquire(2, neverCancel) {
		t.Fatal("initial acquire failed")
	}
	heavyDone := make(chan bool, 1)
	go func() { heavyDone <- limiter.Acquire(4, neverCancel) }()
	deadline := time.Now().Add(time.Second)
	for limiter.queued() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	lightDone := make(chan bool, 1)
	go func() { lightDone <- limiter.Acquire(1, neverCancel) }()
	deadline = time.Now().Add(time.Second)
	for limiter.queued() != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	// Two free units exist, but the newer light waiter must not jump ahead of
	// the older heavy waiter and create indefinite starvation.
	select {
	case <-lightDone:
		t.Fatal("light waiter bypassed older heavy waiter")
	case <-time.After(20 * time.Millisecond):
	}
	limiter.Release(2)
	select {
	case ok := <-heavyDone:
		if !ok {
			t.Fatal("heavy waiter canceled")
		}
	case <-time.After(time.Second):
		t.Fatal("heavy waiter was not granted full capacity")
	}
	select {
	case <-lightDone:
		t.Fatal("light waiter should still wait while heavy owns full capacity")
	case <-time.After(20 * time.Millisecond):
	}
	limiter.Release(4)
	select {
	case ok := <-lightDone:
		if !ok {
			t.Fatal("light waiter canceled")
		}
	case <-time.After(time.Second):
		t.Fatal("light waiter did not run after heavy release")
	}
	limiter.Release(1)
}

func TestGlobalBackpressureCapsProcessesAcrossBatches(t *testing.T) {
	m := resourceTestManager(t, 4, 2)
	root := t.TempDir()
	command := func(id string) string {
		return "printf '" + id + "\\n' >> started; while [ ! -f release ]; do sleep 0.01; done"
	}
	one, err := m.StartBatch(context.Background(), BatchStartRequest{MaxParallel: 2, Jobs: []BatchJobRequest{
		{ID: "a", Command: command("a"), CWD: root, PTY: PTYNever, ResourceClass: ResourceIO},
		{ID: "b", Command: command("b"), CWD: root, PTY: PTYNever, ResourceClass: ResourceIO},
	}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.StartBatch(context.Background(), BatchStartRequest{MaxParallel: 2, Jobs: []BatchJobRequest{
		{ID: "c", Command: command("c"), CWD: root, PTY: PTYNever, ResourceClass: ResourceIO},
		{ID: "d", Command: command("d"), CWD: root, PTY: PTYNever, ResourceClass: ResourceIO},
	}})
	if err != nil {
		t.Fatal(err)
	}

	waitForStartedLines(t, filepath.Join(root, "started"), 2)
	if got := runningManagedSessions(m); got != 2 {
		t.Fatalf("global cap allowed %d live processes, want exactly 2", got)
	}
	if got := m.globalLimiter.inUse(); got != 2 {
		t.Fatalf("global limiter in_use=%d, want 2", got)
	}
	// Cancel waiters before releasing the two live jobs; otherwise a newly freed
	// slot could legitimately start another job between assertions and cleanup.
	if _, err := m.CancelBatch(two.BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelBatch(one.BatchID); err != nil {
		t.Fatal(err)
	}
}

func TestHeavyClassUsesWholeSmallCapacity(t *testing.T) {
	m := resourceTestManager(t, 2, 2)
	root := t.TempDir()
	command := func(id string) string {
		return "printf '" + id + "\\n' >> started; while :; do sleep 1; done"
	}
	batch, err := m.StartBatch(context.Background(), BatchStartRequest{MaxParallel: 2, Jobs: []BatchJobRequest{
		{ID: "a", Command: command("a"), CWD: root, PTY: PTYNever, ResourceClass: ResourceHeavy},
		{ID: "b", Command: command("b"), CWD: root, PTY: PTYNever, ResourceClass: ResourceHeavy},
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitForStartedLines(t, filepath.Join(root, "started"), 1)
	if got := runningManagedSessions(m); got != 1 {
		t.Fatalf("heavy jobs oversubscribed global capacity: running=%d, want 1", got)
	}
	if got := m.globalLimiter.inUse(); got != 2 {
		t.Fatalf("heavy job should consume full capacity, in_use=%d", got)
	}
	if _, err := m.CancelBatch(batch.BatchID); err != nil {
		t.Fatal(err)
	}
}

func resourceTestManager(t *testing.T, batchCap, globalCap int) *Manager {
	t.Helper()
	m, err := NewManager(Options{
		DefaultShell: "/bin/bash", DefaultWaitMS: 50, InitialOutputLines: 20,
		OutputBufferBytes: 1 << 20, MaxLineBytes: 1 << 16, CompletedSessions: 20,
		BatchMaxParallel: batchCap, BatchGlobalParallel: globalCap,
	})
	if err != nil {
		t.Fatal(err)
	}
	m.resourceProbe = func() HostResources { return HostResources{CPUs: 8, MemoryAvailableB: 8 << 30} }
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func waitForStartedLines(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			count := len(strings.Fields(string(data)))
			if count >= want {
				if count != want {
					t.Fatalf("started process count=%d, want %d", count, want)
				}
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d started jobs", want)
}

func runningManagedSessions(m *Manager) int {
	count := 0
	for _, session := range m.ListSessions() {
		if session.ExitCode == nil {
			count++
		}
	}
	return count
}

func TestResourceWaitingJobRemainsQueuedUntilProcessExists(t *testing.T) {
	m := resourceTestManager(t, 2, 2)
	root := t.TempDir()
	command := func(id string) string {
		return "printf '" + id + "\\n' >> started; while [ ! -f release ]; do sleep 0.01; done"
	}
	batch, err := m.StartBatch(context.Background(), BatchStartRequest{MaxParallel: 2, Jobs: []BatchJobRequest{
		{ID: "a", Command: command("a"), CWD: root, PTY: PTYNever, ResourceClass: ResourceHeavy},
		{ID: "b", Command: command("b"), CWD: root, PTY: PTYNever, ResourceClass: ResourceHeavy},
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitForStartedLines(t, filepath.Join(root, "started"), 1)
	snapshot, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: batch.BatchID, OnlyChanged: false, Length: 20})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Counts.Running != 1 || snapshot.Counts.Queued != 1 {
		t.Fatalf("resource admission state lied about concurrency: counts=%+v jobs=%+v", snapshot.Counts, snapshot.Jobs)
	}
	for _, job := range snapshot.Jobs {
		if job.State == BatchJobRunning && job.PID <= 0 {
			t.Fatalf("running job has no PID: %+v", job)
		}
		if job.State == BatchJobQueued && job.PID != 0 {
			t.Fatalf("queued job unexpectedly has PID: %+v", job)
		}
	}
	if _, err := m.CancelBatch(batch.BatchID); err != nil {
		t.Fatal(err)
	}
}

func TestGlobalRunningCountsMatchAdmittedSessionsAcrossBatches(t *testing.T) {
	m := resourceTestManager(t, 2, 2)
	root := t.TempDir()
	command := func(id string) string {
		return "printf '" + id + "\\n' >> started; while [ ! -f release ]; do sleep 0.01; done"
	}
	one, err := m.StartBatch(context.Background(), BatchStartRequest{MaxParallel: 2, Jobs: []BatchJobRequest{
		{ID: "a", Command: command("a"), CWD: root, PTY: PTYNever, ResourceClass: ResourceIO},
		{ID: "b", Command: command("b"), CWD: root, PTY: PTYNever, ResourceClass: ResourceIO},
	}})
	if err != nil {
		t.Fatal(err)
	}
	two, err := m.StartBatch(context.Background(), BatchStartRequest{MaxParallel: 2, Jobs: []BatchJobRequest{
		{ID: "c", Command: command("c"), CWD: root, PTY: PTYNever, ResourceClass: ResourceIO},
		{ID: "d", Command: command("d"), CWD: root, PTY: PTYNever, ResourceClass: ResourceIO},
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitForStartedLines(t, filepath.Join(root, "started"), 2)
	a, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: one.BatchID, OnlyChanged: false, Length: 20})
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.ReadBatch(context.Background(), BatchReadRequest{BatchID: two.BatchID, OnlyChanged: false, Length: 20})
	if err != nil {
		t.Fatal(err)
	}
	if got, live := a.Counts.Running+b.Counts.Running, runningManagedSessions(m); got != live || got != 2 {
		t.Fatalf("reported running=%d live sessions=%d; batch1=%+v batch2=%+v", got, live, a.Counts, b.Counts)
	}
	if _, err := m.CancelBatch(two.BatchID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelBatch(one.BatchID); err != nil {
		t.Fatal(err)
	}
}

func TestCancelRacingSpawnPublishesConsistentTerminalState(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		m := resourceTestManager(t, 1, 1)
		root := t.TempDir()
		batch, err := m.StartBatch(context.Background(), BatchStartRequest{MaxParallel: 1, Jobs: []BatchJobRequest{
			{ID: "race-a", Command: "sleep 30", CWD: root, PTY: PTYNever, ResourceClass: ResourceIO},
			{ID: "race-b", Command: "sleep 30", CWD: root, PTY: PTYNever, ResourceClass: ResourceIO},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.CancelBatch(batch.BatchID); err != nil {
			t.Fatal(err)
		}
		final := waitForBatchState(t, m, batch.BatchID, func(r BatchResult) bool { return r.State != BatchRunning })
		if final.State != BatchCanceled || final.Counts.Running != 0 || final.Counts.Waiting != 0 {
			t.Fatalf("iteration %d inconsistent canceled batch: %+v", iteration, final)
		}
		for _, job := range final.Jobs {
			if job.State != BatchJobCanceled {
				t.Fatalf("iteration %d job escaped cancellation: %+v", iteration, job)
			}
		}
		_ = m.Close()
	}
}
