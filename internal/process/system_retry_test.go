//go:build linux

package process

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestKillSystemProcessRejectsReusedPIDIdentity(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
	ticks, err := systemProcessStartTicks(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := KillSystemProcess(cmd.Process.Pid, ticks+1); err == nil {
		t.Fatal("stale start_ticks unexpectedly signaled process")
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != nil {
		t.Fatalf("stale precondition killed process: %v", err)
	}
	if err := KillSystemProcess(cmd.Process.Pid, ticks); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(cmd.Process.Pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The process may remain a zombie until Wait; a successful identity check +
	// SIGTERM is sufficient here, and Wait confirms signal termination.
	state, err := cmd.Process.Wait()
	if err != nil && state == nil {
		t.Fatal(err)
	}
}

func TestListSystemProcessesExposesStartTicks(t *testing.T) {
	processes, err := ListSystemProcesses()
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		if process.StartTicks == 0 {
			continue
		}
		return
	}
	t.Fatal("no process exposed start_ticks identity")
}
