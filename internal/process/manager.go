package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const defaultInitialOutputLines = 200

type Options struct {
	DefaultShell       string
	DefaultWaitMS      int
	InitialOutputLines int
	OutputBufferBytes  int
	MaxLineBytes       int
	CompletedSessions  int
	BatchMaxParallel   int
}

type Manager struct {
	mu        sync.RWMutex
	sessions  map[int]*session
	completed []int
	opts      Options

	batchMu          sync.Mutex
	batches          map[string]*processBatch
	completedBatches []string
}

func NewManager(opts Options) (*Manager, error) {
	if opts.DefaultShell == "" {
		return nil, errors.New("default shell is required")
	}
	if opts.InitialOutputLines == 0 {
		opts.InitialOutputLines = defaultInitialOutputLines
	}
	if opts.BatchMaxParallel == 0 {
		opts.BatchMaxParallel = 4
	}
	if opts.InitialOutputLines < 0 || opts.OutputBufferBytes <= 0 || opts.MaxLineBytes <= 0 || opts.CompletedSessions < 0 || opts.BatchMaxParallel <= 0 {
		return nil, errors.New("invalid process manager limits")
	}
	return &Manager{sessions: make(map[int]*session), batches: make(map[string]*processBatch), opts: opts}, nil
}

func (m *Manager) Start(ctx context.Context, req StartRequest) (StartResult, error) {
	waitMS := req.TimeoutMS
	if waitMS == 0 {
		waitMS = m.opts.DefaultWaitMS
	}
	if waitMS < 0 {
		return StartResult{}, errors.New("timeout_ms must be >= 0")
	}
	s, err := m.startSession(req.Command, req.CWD, req.Shell, req.PTY, req.SeparateStreams)
	if err != nil {
		return StartResult{}, err
	}

	started := time.Now()
	m.waitForStart(ctx, s, time.Duration(waitMS)*time.Millisecond)
	info := s.snapshot()
	page := m.readInitialOutput(s)
	result := StartResult{
		PID: info.PID, Command: info.Command, CWD: info.CWD, Shell: info.Shell, PTY: info.PTY,
		State: info.State, StartedAt: info.StartedAt, ExitCode: info.ExitCode,
		ReadFrom: page.ReadFrom, ReadCount: page.ReadCount,
		TotalLines: page.TotalLines, Remaining: page.Remaining, EvictedLines: page.EvictedLines,
		WaitedMS: time.Since(started).Milliseconds(), WaitingForInput: info.WaitingForInput,
	}
	if req.SeparateStreams && !info.PTY {
		result.Streams = page.Streams
	} else {
		result.Output = page.Lines
	}
	return result, nil
}

func (m *Manager) startSession(command, cwd, shell string, ptyMode PTYMode, separateStreams bool) (*session, error) {
	if strings.TrimSpace(command) == "" {
		return nil, errors.New("command is required")
	}
	if shell == "" {
		shell = m.opts.DefaultShell
	}
	usePTY, err := resolvePTY(ptyMode, command)
	if err != nil {
		return nil, err
	}
	resolvedCWD, err := resolveCWD(cwd)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(shell, "-l", "-c", command)
	cmd.Dir = resolvedCWD
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	s, err := m.startCommand(cmd, command, resolvedCWD, shell, usePTY, separateStreams)
	if err != nil {
		return nil, err
	}
	m.addSession(s)
	logProcessStarted(s.snapshot())
	return s, nil
}

func resolveCWD(cwd string) (string, error) {
	if cwd == "" {
		return "", nil
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve cwd %q: %w", cwd, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("cwd %q does not exist", cwd)
		}
		return "", fmt.Errorf("stat cwd %q: %w", cwd, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd %q is not a directory", cwd)
	}
	return abs, nil
}

func (m *Manager) startCommand(cmd *exec.Cmd, command, cwd, shell string, usePTY, separateStreams bool) (*session, error) {
	if usePTY {
		ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 120})
		if err != nil {
			return nil, fmt.Errorf("start PTY process: %w", err)
		}
		s := newSession(cmd, ptmx, ptmx, command, cwd, shell, true, false, m.opts.OutputBufferBytes, m.opts.MaxLineBytes)
		s.ptyFile = ptmx
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
	s := newSession(cmd, stdin, stdin, command, cwd, shell, false, separateStreams, m.opts.OutputBufferBytes, m.opts.MaxLineBytes)
	// Assign writers directly instead of using StdoutPipe/StderrPipe. os/exec then
	// owns the copy goroutines and Cmd.Wait does not return until their final bytes
	// are delivered to sessionWriter. This avoids the documented Wait-vs-Read race
	// for very short-lived commands.
	cmd.Stdout = sessionWriter{s: s, stream: "stdout"}
	cmd.Stderr = sessionWriter{s: s, stream: "stderr"}
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
			s.feed(buf[:n], "pty")
		}
		if err != nil {
			return
		}
	}
}

func (m *Manager) wait(s *session) {
	err := s.cmd.Wait()
	if s.usePTY {
		s.mu.Lock()
		if s.closer != nil {
			_ = s.closer.Close()
			s.closer = nil
			s.ptyFile = nil
		}
		s.mu.Unlock()
		s.captureWG.Wait()
	}
	s.markExited(err)
	logProcessExited(s.snapshot(), err)
	m.markCompleted(s.pid)
}

type sessionWriter struct {
	s      *session
	stream string
}

func (w sessionWriter) Write(p []byte) (int, error) {
	w.s.feed(p, w.stream)
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

type initialOutputPage struct {
	Lines        []string
	Streams      []StreamLine
	ReadFrom     int
	ReadCount    int
	TotalLines   int
	Remaining    int
	EvictedLines int64
}

func (m *Manager) readInitialOutput(s *session) initialOutputPage {
	s.mu.Lock()
	defer s.mu.Unlock()

	lines := s.snapshotLinesLocked()
	retainedStart := s.evictedLines
	total := retainedStart + int64(len(lines))
	end := retainedStart + int64(m.opts.InitialOutputLines)
	if end > total {
		end = total
	}
	localEnd := int(end - retainedStart)
	selected := append([]string(nil), lines[:localEnd]...)
	streamLines := s.snapshotStreamsLocked()
	selectedStreams := append([]StreamLine(nil), streamLines[:localEnd]...)
	if end > s.lastReadAbs {
		s.lastReadAbs = end
	}
	s.lastReadGeneration = s.outputGeneration
	return initialOutputPage{
		Lines: selected, Streams: selectedStreams, ReadFrom: int(retainedStart), ReadCount: len(selected), TotalLines: int(total),
		Remaining: int(total - end), EvictedLines: s.evictedLines,
	}
}
