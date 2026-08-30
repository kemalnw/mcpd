package durableexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

func RunRunner(statePath string, input io.Reader) error {
	dec := json.NewDecoder(io.LimitReader(input, 1<<20))
	dec.DisallowUnknownFields()
	var spec runnerSpec
	if err := dec.Decode(&spec); err != nil {
		return fmt.Errorf("decode runner spec: %w", err)
	}
	if spec.JobID == "" || spec.Command == "" || spec.Shell == "" || spec.LogPath == "" {
		return errors.New("invalid runner spec")
	}
	boot, err := bootID()
	if err != nil {
		return err
	}
	runnerTicks, err := processStartTicks(os.Getpid())
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	sum := sha256.Sum256([]byte(spec.Command))
	job := Job{SchemaVersion: SchemaVersion, ID: spec.JobID, State: StateStarting, RunnerPID: os.Getpid(), RunnerStartTicks: runnerTicks, BootID: boot, CommandSHA256: hex.EncodeToString(sum[:]), CommandBytes: len(spec.Command), CWD: spec.CWD, Shell: spec.Shell, StartedAt: now, UpdatedAt: now, LogPath: spec.LogPath}
	if err := writeJob(statePath, job); err != nil {
		return err
	}

	logFile, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open durable job log: %w", err)
	}
	defer logFile.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd := exec.Command(spec.Shell, "-l", "-c", spec.Command)
	cmd.Dir = spec.CWD
	cmd.Env = append(os.Environ(), "TERM=dumb")
	cmd.Stdin = devnull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		finished := time.Now().UTC()
		job.State = StateFailed
		job.Reason = "start_failed"
		job.UpdatedAt = finished
		job.FinishedAt = &finished
		_ = writeJob(statePath, job)
		return fmt.Errorf("start durable child: %w", err)
	}
	childTicks, err := processStartTicks(cmd.Process.Pid)
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return err
	}
	job.ChildPID = cmd.Process.Pid
	job.ChildStartTicks = childTicks
	job.State = StateRunning
	job.UpdatedAt = time.Now().UTC()
	if err := writeJob(statePath, job); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return err
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sigCh)
	canceled := false
	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-sigCh:
		canceled = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case waitErr = <-waitCh:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			waitErr = <-waitCh
		}
	}
	_ = logFile.Sync()
	finished := time.Now().UTC()
	job.UpdatedAt = finished
	job.FinishedAt = &finished
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		job.ExitCode = &code
	}
	if canceled {
		job.State = StateCanceled
		job.Reason = "cancel_requested"
	} else if waitErr == nil {
		job.State = StateCompleted
	} else {
		job.State = StateFailed
		job.Reason = "process_failed"
	}
	if err := writeJob(statePath, job); err != nil {
		return err
	}
	return nil
}

func waitForState(ctx context.Context, path string, predicate func(Job) bool) (Job, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := readJob(path)
		if err == nil && predicate(job) {
			return job, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Job{}, err
		}
		select {
		case <-ctx.Done():
			return Job{}, ctx.Err()
		case <-ticker.C:
		}
	}
}
