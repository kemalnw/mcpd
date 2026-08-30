package durableexec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Manager struct {
	root, socket string
	now          func() time.Time
}

func Open(root, socket string) (*Manager, error) {
	if root == "" || socket == "" {
		return nil, errors.New("durable execution root and supervisor socket are required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "jobs"), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "logs"), 0o700); err != nil {
		return nil, err
	}
	return &Manager{root: abs, socket: socket, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (m *Manager) Start(ctx context.Context, req StartRequest) (Job, bool, error) {
	if strings.TrimSpace(req.Command) == "" {
		return Job{}, false, errors.New("command is required")
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", m.socket)
	if err != nil {
		return Job{}, false, fmt.Errorf("connect durable supervisor: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := json.NewEncoder(conn).Encode(supervisorRequest{Version: supervisorProtocolVersion, Op: "start", Start: req}); err != nil {
		return Job{}, false, fmt.Errorf("submit durable job: %w", err)
	}
	dec := json.NewDecoder(io.LimitReader(conn, 256<<10))
	dec.DisallowUnknownFields()
	var resp supervisorResponse
	if err := dec.Decode(&resp); err != nil {
		return Job{}, false, fmt.Errorf("read durable supervisor response: %w", err)
	}
	if resp.Version != supervisorProtocolVersion {
		return Job{}, false, errors.New("durable supervisor protocol mismatch")
	}
	if resp.Error != "" {
		return Job{}, false, errors.New(resp.Error)
	}
	return resp.Job, resp.IdempotentReplay, nil
}

func (m *Manager) Get(id string) (Job, error) {
	if !validJobID(id) {
		return Job{}, errors.New("invalid durable job id")
	}
	return readJob(m.statePath(id))
}
func (m *Manager) List() ([]Job, error) {
	entries, err := os.ReadDir(filepath.Join(m.root, "jobs"))
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		job, err := readJob(filepath.Join(m.root, "jobs", e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}
func (m *Manager) Reconcile() ([]Job, error) {
	jobs, err := m.List()
	if err != nil {
		return nil, err
	}
	currentBoot, err := bootID()
	if err != nil {
		return nil, err
	}
	for i := range jobs {
		job := &jobs[i]
		if terminal(job.State) {
			continue
		}
		reason := ""
		if job.BootID != currentBoot {
			reason = "vm_reboot"
		} else if !processIdentityMatches(job.RunnerPID, job.RunnerStartTicks) {
			reason = "runner_missing"
		}
		if reason == "" {
			continue
		}
		now := m.now()
		job.State = StateOrphaned
		job.Reason = reason
		job.UpdatedAt = now
		job.FinishedAt = &now
		if err := writeJob(m.statePath(job.ID), *job); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}
func (m *Manager) Cancel(ctx context.Context, id string) (Job, error) {
	job, err := m.Get(id)
	if err != nil {
		return Job{}, err
	}
	if terminal(job.State) {
		return job, nil
	}
	currentBoot, err := bootID()
	if err != nil {
		return Job{}, err
	}
	if job.BootID != currentBoot || !processIdentityMatches(job.RunnerPID, job.RunnerStartTicks) {
		now := m.now()
		job.State = StateOrphaned
		job.Reason = "runner_missing"
		job.UpdatedAt = now
		job.FinishedAt = &now
		_ = writeJob(m.statePath(id), job)
		return job, nil
	}
	if err := syscall.Kill(job.RunnerPID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return Job{}, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	return waitForState(waitCtx, m.statePath(id), func(j Job) bool { return terminal(j.State) })
}
func (m *Manager) ReadLogTail(id string, maxBytes int) (LogTail, error) {
	job, err := m.Get(id)
	if err != nil {
		return LogTail{}, err
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 10
	}
	if maxBytes > 1<<20 {
		maxBytes = 1 << 20
	}
	f, err := os.Open(job.LogPath)
	if errors.Is(err, os.ErrNotExist) {
		return LogTail{JobID: id}, nil
	}
	if err != nil {
		return LogTail{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return LogTail{}, err
	}
	start := info.Size() - int64(maxBytes)
	if start < 0 {
		start = 0
	}
	buf := make([]byte, info.Size()-start)
	_, err = f.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return LogTail{}, err
	}
	return LogTail{JobID: id, Content: string(buf), BytesReturned: len(buf), TotalBytes: info.Size(), StartOffset: start, Truncated: start > 0}, nil
}

func (m *Manager) statePath(id string) string { return filepath.Join(m.root, "jobs", id+".json") }
func terminal(s State) bool {
	return s == StateCompleted || s == StateFailed || s == StateCanceled || s == StateOrphaned
}
func validJobID(id string) bool {
	if !strings.HasPrefix(id, "job_") || len(id) > 64 {
		return false
	}
	for _, r := range id[4:] {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return len(id) > 4
}
func newJobID() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "job_" + hex.EncodeToString(b), nil
}
