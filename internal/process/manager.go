package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

type Options struct {
	DefaultShell      string
	DefaultWaitMS     int
	OutputBufferBytes int
	MaxLineBytes      int
	CompletedSessions int
}

type Manager struct {
	mu        sync.RWMutex
	sessions  map[int]*session
	completed []int
	opts      Options
}

func NewManager(opts Options) (*Manager, error) {
	if opts.DefaultShell == "" {
		return nil, errors.New("default shell is required")
	}
	if opts.OutputBufferBytes <= 0 || opts.MaxLineBytes <= 0 || opts.CompletedSessions < 0 {
		return nil, errors.New("invalid process manager limits")
	}
	return &Manager{sessions: make(map[int]*session), opts: opts}, nil
}

func (m *Manager) Start(ctx context.Context, req StartRequest) (StartResult, error) {
	if req.Command == "" {
		return StartResult{}, errors.New("command is required")
	}
	shell := req.Shell
	if shell == "" {
		shell = m.opts.DefaultShell
	}
	waitMS := req.TimeoutMS
	if waitMS == 0 {
		waitMS = m.opts.DefaultWaitMS
	}
	if waitMS < 0 {
		return StartResult{}, errors.New("timeout_ms must be >= 0")
	}
	usePTY, err := resolvePTY(req.PTY, req.Command)
	if err != nil {
		return StartResult{}, err
	}

	cmd := exec.Command(shell, "-l", "-c", req.Command)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	s, err := m.startCommand(cmd, req.Command, shell, usePTY)
	if err != nil {
		return StartResult{}, err
	}
	m.addSession(s)
	logProcessStarted(s.snapshot())

	started := time.Now()
	m.waitForStart(ctx, s, time.Duration(waitMS)*time.Millisecond)
	info := s.snapshot()
	lines := m.readAllRetained(s)
	return StartResult{
		PID: info.PID, Command: info.Command, Shell: info.Shell, PTY: info.PTY,
		State: info.State, StartedAt: info.StartedAt, ExitCode: info.ExitCode,
		Output: lines, WaitedMS: time.Since(started).Milliseconds(), WaitingForInput: info.WaitingForInput,
	}, nil
}

func (m *Manager) startCommand(cmd *exec.Cmd, command, shell string, usePTY bool) (*session, error) {
	if usePTY {
		ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
		if err != nil {
			return nil, fmt.Errorf("start PTY process: %w", err)
		}
		s := newSession(cmd, ptmx, ptmx, command, shell, true, m.opts.OutputBufferBytes, m.opts.MaxLineBytes)
		s.pid = cmd.Process.Pid
		s.captureWG.Add(1)
		go m.capture(s, ptmx)
		go m.wait(s)
		return s, nil
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	s := newSession(cmd, stdin, stdin, command, shell, false, m.opts.OutputBufferBytes, m.opts.MaxLineBytes)
	// Assign writers directly instead of using StdoutPipe/StderrPipe. os/exec then
	// owns the copy goroutines and Cmd.Wait does not return until their final bytes
	// are delivered to sessionWriter. This avoids the documented Wait-vs-Read race
	// for very short-lived commands.
	cmd.Stdout = sessionWriter{s: s}
	cmd.Stderr = sessionWriter{s: s}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}
	s.pid = cmd.Process.Pid
	go m.wait(s)
	return s, nil
}

func (m *Manager) capture(s *session, reader io.Reader) {
	defer s.captureWG.Done()
	buf := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			s.feed(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

func (m *Manager) wait(s *session) {
	err := s.cmd.Wait()
	if s.usePTY {
		if s.closer != nil {
			_ = s.closer.Close()
		}
		s.captureWG.Wait()
	}
	s.markExited(err)
	logProcessExited(s.snapshot(), err)
	m.markCompleted(s.pid)
}

type sessionWriter struct{ s *session }

func (w sessionWriter) Write(p []byte) (int, error) {
	w.s.feed(p)
	return len(p), nil
}

func (m *Manager) waitForStart(ctx context.Context, s *session, timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		info := s.snapshot()
		if info.State == StateExited || info.WaitingForInput {
			return
		}
		ch := s.currentNotify()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ch:
		}
	}
}

func (m *Manager) addSession(s *session) {
	m.mu.Lock()
	m.sessions[s.pid] = s
	m.mu.Unlock()
}

func (m *Manager) markCompleted(pid int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completed = append(m.completed, pid)
	for len(m.completed) > m.opts.CompletedSessions {
		oldest := m.completed[0]
		m.completed = m.completed[1:]
		delete(m.sessions, oldest)
	}
}

func (m *Manager) get(pid int) (*session, error) {
	m.mu.RLock()
	s := m.sessions[pid]
	m.mu.RUnlock()
	if s == nil {
		return nil, ErrSessionNotFound
	}
	return s, nil
}

func (m *Manager) ListSessions() []SessionInfo {
	m.mu.RLock()
	sessions := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()
	out := make([]SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out
}

func (m *Manager) readAllRetained(s *session) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLinesLocked()
}
