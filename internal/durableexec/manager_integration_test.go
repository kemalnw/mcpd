//go:build linux

package durableexec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	testBinaryOnce sync.Once
	testBinaryDir  string
	testBinaryPath string
	testBinaryErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if testBinaryDir != "" {
		_ = os.RemoveAll(testBinaryDir)
	}
	os.Exit(code)
}

func buildMCPDBinary(t *testing.T) string {
	t.Helper()
	testBinaryOnce.Do(func() {
		testBinaryDir, testBinaryErr = os.MkdirTemp("", "mcpd-durable-test-*")
		if testBinaryErr != nil {
			return
		}
		testBinaryPath = filepath.Join(testBinaryDir, "mcpd")
		cmd := exec.Command("go", "build", "-o", testBinaryPath, "../../cmd/mcpd")
		cmd.Dir = "."
		if output, err := cmd.CombinedOutput(); err != nil {
			testBinaryErr = fmt.Errorf("build mcpd: %w\n%s", err, output)
		}
	})
	if testBinaryErr != nil {
		t.Fatal(testBinaryErr)
	}
	return testBinaryPath
}

func TestDetachedJobSurvivesManagerReopenAndReconciles(t *testing.T) {
	binary := buildMCPDBinary(t)
	root := t.TempDir()
	socket := SupervisorSocket(root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- ServeSupervisor(ctx, root, socket, binary) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	first, err := Open(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	job, replay, err := first.Start(context.Background(), StartRequest{Command: "printf 'first\\n'; sleep .4; printf 'second\\n'", Shell: "/bin/bash"})
	if err != nil {
		t.Fatal(err)
	}
	if replay {
		t.Fatal("first durable start reported replay")
	}
	if job.State != StateRunning || job.RunnerPID <= 0 || job.ChildPID <= 0 {
		t.Fatalf("initial job=%+v", job)
	}

	// Reopen the durable state as a fresh daemon instance. The first Manager has
	// no lifecycle ownership of the detached runner and can disappear safely.
	second, err := Open(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	reconciled, err := second.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if len(reconciled) != 1 || reconciled[0].ID != job.ID || reconciled[0].State != StateRunning {
		t.Fatalf("reconcile lost surviving detached job: %+v", reconciled)
	}

	deadline = time.Now().Add(5 * time.Second)
	var final Job
	for time.Now().Before(deadline) {
		final, err = second.Get(job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if terminal(final.State) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.State != StateCompleted || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("durable job did not complete after manager reopen: %+v", final)
	}
	data, err := os.ReadFile(final.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, "first\n") || !strings.Contains(got, "second\n") {
		t.Fatalf("durable log incomplete: %q", got)
	}
}

func TestReconcileRejectsStalePIDIdentity(t *testing.T) {
	root := t.TempDir()
	manager, err := Open(root, SupervisorSocket(root))
	if err != nil {
		t.Fatal(err)
	}
	boot, err := bootID()
	if err != nil {
		t.Fatal(err)
	}
	ticks, err := processStartTicks(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job := Job{SchemaVersion: SchemaVersion, ID: "job_deadbeef", State: StateRunning, RunnerPID: os.Getpid(), RunnerStartTicks: ticks + 1, BootID: boot, CommandSHA256: strings.Repeat("0", 64), CommandBytes: 1, Shell: "/bin/bash", StartedAt: now, UpdatedAt: now, LogPath: filepath.Join(root, "logs", "job_deadbeef.log")}
	if err := writeJob(manager.statePath(job.ID), job); err != nil {
		t.Fatal(err)
	}
	jobs, err := manager.Reconcile()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].State != StateOrphaned || jobs[0].Reason != "runner_missing" {
		t.Fatalf("stale PID identity remained live: %+v", jobs)
	}
}

func TestDurableStartIdempotencySurvivesClientReopen(t *testing.T) {
	binary := buildMCPDBinary(t)
	root := t.TempDir()
	socket := SupervisorSocket(root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeSupervisor(ctx, root, socket, binary) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	first, err := Open(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{Command: "printf 'once\\n'; sleep .3", Shell: "/bin/bash", IdempotencyKey: "retry-one"}
	one, replay, err := first.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if replay {
		t.Fatal("first start reported replay")
	}
	second, err := Open(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	two, replay, err := second.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay || two.ID != one.ID || two.RunnerPID != one.RunnerPID {
		t.Fatalf("replay=%v one=%+v two=%+v", replay, one, two)
	}
	if _, _, err := second.Start(context.Background(), StartRequest{Command: "printf changed", Shell: "/bin/bash", IdempotencyKey: "retry-one"}); err == nil {
		t.Fatal("conflicting durable idempotency key was accepted")
	}
	data, err := os.ReadFile(filepath.Join(root, "idempotency", mustStartKeyDigest(t, "retry-one")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "retry-one") || strings.Contains(string(data), request.Command) {
		t.Fatalf("durable idempotency record persisted raw key/command: %s", data)
	}
}

func TestPendingDurableIdempotencyRecordRecoversSameJobID(t *testing.T) {
	binary := buildMCPDBinary(t)
	root := t.TempDir()
	socket := SupervisorSocket(root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = ServeSupervisor(ctx, root, socket, binary) }()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	request := StartRequest{Command: "printf recovered\\n", Shell: "/bin/bash", IdempotencyKey: "pending"}
	fingerprint, err := startRequestFingerprint(request)
	if err != nil {
		t.Fatal(err)
	}
	jobID := "job_0123456789abcdefabcd"
	digest := mustStartKeyDigest(t, request.IdempotencyKey)
	if err := writeStartIdempotency(idempotencyPath(root, digest), startIdempotencyRecord{SchemaVersion: SchemaVersion, Fingerprint: fingerprint, JobID: jobID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	manager, err := Open(root, socket)
	if err != nil {
		t.Fatal(err)
	}
	job, replay, err := manager.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replay || job.ID != jobID {
		t.Fatalf("pending record recovery=%+v replay=%v", job, replay)
	}
}

func mustStartKeyDigest(t *testing.T, key string) string {
	t.Helper()
	digest, err := startKeyDigest(key)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
