package process

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type session struct {
	mu sync.Mutex

	cmd             *exec.Cmd
	stdin           io.Writer
	closer          io.Closer
	pid             int
	command         string
	cwd             string
	shell           string
	usePTY          bool
	separateStreams bool
	ptyFile         *os.File

	startedAt time.Time
	state     State
	exitCode  *int
	waitErr   error

	lines              []string
	lineStreams        []string
	partial            string
	partialStream      string
	bufferBytes        int
	maxBytes           int
	maxLineBytes       int
	evictedLines       int64
	evictedChars       int64
	lastReadAbs        int64
	outputGeneration   uint64
	lastReadGeneration uint64

	waitingForInput  bool
	promptGeneration uint64
	tail             string

	done      chan struct{}
	doneOnce  sync.Once
	captureWG sync.WaitGroup
	notify    chan struct{}
}

func newSession(cmd *exec.Cmd, stdin io.Writer, closer io.Closer, command, cwd, shell string, usePTY, separateStreams bool, maxBytes, maxLineBytes int) *session {
	return &session{
		cmd: cmd, stdin: stdin, closer: closer, pid: 0,
		command: command, cwd: cwd, shell: shell, usePTY: usePTY, separateStreams: separateStreams,
		startedAt: time.Now().UTC(), state: StateRunning,
		maxBytes: maxBytes, maxLineBytes: maxLineBytes,
		done: make(chan struct{}), notify: make(chan struct{}),
	}
}

func (s *session) feed(data []byte, stream string) {
	if len(data) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.separateStreams {
		stream = ""
	}
	if s.partial != "" && s.separateStreams && s.partialStream != "" && stream != s.partialStream {
		s.appendLineLocked(strings.TrimSuffix(s.partial, "\r"), s.partialStream)
		s.partial = ""
		s.partialStream = ""
	}
	raw := s.partial + string(data)
	parts := strings.Split(raw, "\n")
	s.partial = parts[len(parts)-1]
	if s.partial != "" {
		s.partialStream = stream
	} else {
		s.partialStream = ""
	}
	for _, line := range parts[:len(parts)-1] {
		s.appendLineLocked(strings.TrimSuffix(line, "\r"), stream)
	}
	if len(s.partial) > s.maxLineBytes {
		s.partial = s.partial[:s.maxLineBytes] + " [line truncated]"
	}

	s.tail += string(data)
	if len(s.tail) > 4096 {
		s.tail = s.tail[len(s.tail)-4096:]
	}
	s.outputGeneration++
	s.promptGeneration++
	s.waitingForInput = false
	if s.state == StateWaiting {
		s.state = StateRunning
	}
	promptCandidate := looksLikePrompt(s.tail)
	generation := s.promptGeneration
	s.enforceLimitLocked()
	s.signalLocked()
	if promptCandidate {
		time.AfterFunc(promptStabilityDelayFor(s.command), func() { s.confirmPrompt(generation) })
	}
}

func (s *session) confirmPrompt(generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if generation != s.promptGeneration || s.exitCode != nil || !looksLikePrompt(s.tail) {
		return
	}
	s.waitingForInput = true
	if s.state == StateRunning {
		s.state = StateWaiting
	}
	s.signalLocked()
}

func (s *session) appendLineLocked(line, stream string) {
	if len(line) > s.maxLineBytes {
		line = line[:s.maxLineBytes] + " [line truncated]"
	}
	s.lines = append(s.lines, line)
	s.lineStreams = append(s.lineStreams, stream)
	s.bufferBytes += len(line) + 1
}

func (s *session) enforceLimitLocked() {
	for s.bufferBytes > s.maxBytes && len(s.lines) > 0 {
		evictedBytes := len(s.lines[0]) + 1
		s.bufferBytes -= evictedBytes
		s.evictedChars += int64(evictedBytes)
		s.lines[0] = ""
		s.lines = s.lines[1:]
		if len(s.lineStreams) > 0 {
			s.lineStreams = s.lineStreams[1:]
		}
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
		s.appendLineLocked(strings.TrimSuffix(s.partial, "\r"), s.partialStream)
		s.partial = ""
		s.partialStream = ""
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
	s.promptGeneration++
	s.waitingForInput = false
	if s.state == StateRunning || s.state == StateWaiting {
		s.state = StateStopping
		s.signalLocked()
	}
	s.mu.Unlock()
}

func (s *session) write(input string, raw bool) error {
	s.mu.Lock()
	if s.exitCode != nil {
		s.mu.Unlock()
		return ErrProcessExited
	}
	s.promptGeneration++
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
	if !raw && !strings.HasSuffix(input, "\n") {
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
		PID: s.pid, Command: s.command, CWD: s.cwd, Shell: s.shell, PTY: s.usePTY, SeparateStreams: s.separateStreams,
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

func (s *session) snapshotStreamsLocked() []StreamLine {
	out := make([]StreamLine, 0, len(s.lines)+1)
	for i, line := range s.lines {
		stream := ""
		if i < len(s.lineStreams) {
			stream = s.lineStreams[i]
		}
		out = append(out, StreamLine{Stream: stream, Text: line})
	}
	if s.partial != "" {
		out = append(out, StreamLine{Stream: s.partialStream, Text: s.partial})
	}
	return out
}

func (s *session) latestLineLocked() *StreamLine {
	if s.partial != "" {
		return &StreamLine{Stream: s.partialStream, Text: s.partial}
	}
	if len(s.lines) == 0 {
		return nil
	}
	i := len(s.lines) - 1
	stream := ""
	if i < len(s.lineStreams) {
		stream = s.lineStreams[i]
	}
	return &StreamLine{Stream: stream, Text: s.lines[i]}
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
