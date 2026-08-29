package process

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type session struct {
	mu sync.Mutex

	cmd     *exec.Cmd
	stdin   io.Writer
	closer  io.Closer
	pid     int
	command string
	shell   string
	usePTY  bool

	startedAt time.Time
	state     State
	exitCode  *int
	waitErr   error

	lines        []string
	partial      string
	bufferBytes  int
	maxBytes     int
	maxLineBytes int
	evictedLines int64
	evictedChars int64
	lastReadAbs  int64

	waitingForInput bool
	tail            string

	done     chan struct{}
	doneOnce sync.Once
	notify   chan struct{}
}

func newSession(cmd *exec.Cmd, stdin io.Writer, closer io.Closer, command, shell string, usePTY bool, maxBytes, maxLineBytes int) *session {
	return &session{
		cmd: cmd, stdin: stdin, closer: closer, pid: cmd.Process.Pid,
		command: command, shell: shell, usePTY: usePTY,
		startedAt: time.Now().UTC(), state: StateRunning,
		maxBytes: maxBytes, maxLineBytes: maxLineBytes,
		done: make(chan struct{}), notify: make(chan struct{}),
	}
}

func (s *session) feed(data []byte) {
	if len(data) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	raw := s.partial + string(data)
	parts := strings.Split(raw, "\n")
	s.partial = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		s.appendLineLocked(strings.TrimSuffix(line, "\r"))
	}
	if len(s.partial) > s.maxLineBytes {
		s.partial = s.partial[:s.maxLineBytes] + " [line truncated]"
	}

	s.tail += string(data)
	if len(s.tail) > 4096 {
		s.tail = s.tail[len(s.tail)-4096:]
	}
	s.waitingForInput = looksLikePrompt(s.tail)
	if s.waitingForInput && s.state == StateRunning {
		s.state = StateWaiting
	} else if !s.waitingForInput && s.state == StateWaiting {
		s.state = StateRunning
	}
	s.enforceLimitLocked()
	s.signalLocked()
}

func (s *session) appendLineLocked(line string) {
	if len(line) > s.maxLineBytes {
		line = line[:s.maxLineBytes] + " [line truncated]"
	}
	s.lines = append(s.lines, line)
	s.bufferBytes += len(line) + 1
}

func (s *session) enforceLimitLocked() {
	for s.bufferBytes > s.maxBytes && len(s.lines) > 0 {
		evictedBytes := len(s.lines[0]) + 1
		s.bufferBytes -= evictedBytes
		s.evictedChars += int64(evictedBytes)
		s.lines[0] = ""
		s.lines = s.lines[1:]
		s.evictedLines++
	}
	if s.lastReadAbs < s.evictedLines {
		s.lastReadAbs = s.evictedLines
	}
}

func (s *session) signalLocked() {
	close(s.notify)
	s.notify = make(chan struct{})
}

func (s *session) markExited(err error) {
	s.mu.Lock()
	if s.partial != "" {
		s.appendLineLocked(strings.TrimSuffix(s.partial, "\r"))
		s.partial = ""
	}
	code := -1
	if s.cmd.ProcessState != nil {
		code = s.cmd.ProcessState.ExitCode()
	}
	s.exitCode = &code
	s.waitErr = err
	s.waitingForInput = false
	s.state = StateExited
	s.enforceLimitLocked()
	s.signalLocked()
	s.mu.Unlock()

	s.doneOnce.Do(func() { close(s.done) })
}

func (s *session) markStopping() {
	s.mu.Lock()
	if s.state == StateRunning || s.state == StateWaiting {
		s.state = StateStopping
		s.signalLocked()
	}
	s.mu.Unlock()
}

func (s *session) write(input string) error {
	s.mu.Lock()
	if s.exitCode != nil {
		s.mu.Unlock()
		return ErrProcessExited
	}
	s.waitingForInput = false
	if s.state == StateWaiting {
		s.state = StateRunning
	}
	writer := s.stdin
	s.signalLocked()
	s.mu.Unlock()

	if writer == nil {
		return fmt.Errorf("process %d has no writable stdin", s.pid)
	}
	if !strings.HasSuffix(input, "\n") {
		input += "\n"
	}
	_, err := io.WriteString(writer, input)
	return err
}

func (s *session) snapshot() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := len(s.lines)
	if s.partial != "" {
		lines++
	}
	return SessionInfo{
		PID: s.pid, Command: s.command, Shell: s.shell, PTY: s.usePTY,
		State: s.state, StartedAt: s.startedAt, RuntimeMS: time.Since(s.startedAt).Milliseconds(),
		ExitCode: cloneInt(s.exitCode), TotalLines: int(s.evictedLines) + lines,
		EvictedLines: s.evictedLines, WaitingForInput: s.waitingForInput,
	}
}

func (s *session) currentNotify() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.notify
}

func (s *session) totalLinesLocked() int64 {
	total := s.evictedLines + int64(len(s.lines))
	if s.partial != "" {
		total++
	}
	return total
}

func (s *session) snapshotLinesLocked() []string {
	out := make([]string, 0, len(s.lines)+1)
	out = append(out, s.lines...)
	if s.partial != "" {
		out = append(out, s.partial)
	}
	return out
}

func (s *session) fullOutputLocked() string {
	if len(s.lines) == 0 {
		return s.partial
	}
	full := strings.Join(s.lines, "\n")
	if s.partial != "" {
		full += "\n" + s.partial
	}
	return full
}

func (s *session) totalCharsLocked() int64 {
	return s.evictedChars + int64(len(s.fullOutputLocked()))
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	v := *value
	return &v
}
