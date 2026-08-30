package process

import (
	"context"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/creack/pty"
)

const (
	defaultReadTimeoutMS     = 5000
	defaultInteractTimeoutMS = 8000
	defaultReadLength        = 1000
	terminateGrace           = time.Second
)

func (m *Manager) ReadOutput(ctx context.Context, req OutputRequest) (OutputResult, error) {
	s, err := m.get(req.PID)
	if err != nil {
		return OutputResult{}, err
	}
	if req.TimeoutMS < 0 {
		return OutputResult{}, errors.New("timeout_ms must be >= 0")
	}
	if req.TimeoutMS == 0 {
		req.TimeoutMS = defaultReadTimeoutMS
	}
	if req.Length <= 0 {
		req.Length = defaultReadLength
	}
	maxBytes, err := effectiveResponseBytes(req.MaxBytes, m.opts.ResponseOutputBytes)
	if err != nil {
		return OutputResult{}, err
	}

	if req.Offset == 0 {
		m.waitForUnreadOutput(ctx, s, time.Duration(req.TimeoutMS)*time.Millisecond)
	}
	return readPaginated(s, req.Offset, req.Length, maxBytes), nil
}

func (m *Manager) waitForUnreadOutput(ctx context.Context, s *session, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		total := s.totalLinesLocked()
		unread := total > s.lastReadAbs || s.outputGeneration > s.lastReadGeneration
		exited := s.exitCode != nil
		ch := s.notify
		s.mu.Unlock()
		if unread || exited {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ch:
		}
	}
}

func readPaginated(s *session, offset, length, maxBytes int) OutputResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	lines := s.snapshotLinesLocked()
	streamLines := s.snapshotStreamsLocked()
	retainedStart := s.evictedLines
	total := retainedStart + int64(len(lines))
	generationChanged := s.outputGeneration > s.lastReadGeneration
	var start int64
	switch {
	case offset == 0:
		start = s.lastReadAbs
	case offset > 0:
		start = int64(offset)
	default:
		start = total + int64(offset)
	}
	if start < retainedStart {
		start = retainedStart
	}
	if start > total {
		start = total
	}
	end := start + int64(length)
	if end > total {
		end = total
	}

	localStart := int(start - retainedStart)
	localEnd := int(end - retainedStart)
	budget := fitForwardBudget(lines[localStart:localEnd], streamLines[localStart:localEnd], s.separateStreams && !s.usePTY, maxBytes)
	end = start + int64(budget.consumedLines)
	selected := budget.lines
	selectedStreams := budget.streams
	var latest *StreamLine
	if offset == 0 {
		if len(selected) == 0 && generationChanged {
			latest = s.latestLineLocked()
		}
		s.lastReadAbs = end
		s.lastReadGeneration = s.outputGeneration
	}
	state := s.state
	if s.waitingForInput && s.exitCode == nil {
		state = StateWaiting
	}
	result := OutputResult{
		PID: s.pid, State: state, ExitCode: cloneInt(s.exitCode), Generation: s.outputGeneration,
		LatestLine: latest, ReadFrom: int(start), ReadCount: len(selected), TotalLines: int(total), Remaining: int(total - end),
		BytesReturned: budget.bytesReturned, OutputTruncated: budget.truncated, OmittedBytes: budget.omittedBytes, EvictedLines: s.evictedLines, WaitingForInput: s.waitingForInput,
		RuntimeMS: time.Since(s.startedAt).Milliseconds(),
	}
	if s.separateStreams && !s.usePTY {
		result.Streams = selectedStreams
	} else {
		result.Lines = selected
	}
	return result
}

func (m *Manager) Interact(ctx context.Context, req InteractRequest) (InteractResult, error) {
	s, err := m.get(req.PID)
	if err != nil {
		return InteractResult{}, err
	}
	if req.TimeoutMS < 0 {
		return InteractResult{}, errors.New("timeout_ms must be >= 0")
	}
	if req.TimeoutMS == 0 {
		req.TimeoutMS = defaultInteractTimeoutMS
	}

	s.mu.Lock()
	snapshotChars := s.totalCharsLocked()
	s.mu.Unlock()
	if err := s.write(req.Input, req.RawInput); err != nil {
		return InteractResult{}, fmt.Errorf("write to process %d: %w", req.PID, err)
	}
	if req.WaitForPrompt {
		m.waitForInteraction(ctx, s, snapshotChars, time.Duration(req.TimeoutMS)*time.Millisecond)
	}

	s.mu.Lock()
	newOutput := outputSinceSnapshotLocked(s, snapshotChars)
	state := s.state
	if s.waitingForInput && s.exitCode == nil {
		state = StateWaiting
	}
	result := InteractResult{
		PID: s.pid, State: state, ExitCode: cloneInt(s.exitCode),
		WaitingForInput: s.waitingForInput, RuntimeMS: time.Since(s.startedAt).Milliseconds(),
	}
	if newOutput != "" {
		result.Lines = splitOutputLines(newOutput)
	}
	s.mu.Unlock()
	return result, nil
}

func (m *Manager) waitForInteraction(ctx context.Context, s *session, snapshotChars int64, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		s.mu.Lock()
		changed := s.totalCharsLocked() > snapshotChars
		waiting := s.waitingForInput
		exited := s.exitCode != nil
		ch := s.notify
		s.mu.Unlock()
		if exited || (changed && waiting) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ch:
		}
	}
}

func outputSinceSnapshotLocked(s *session, snapshotChars int64) string {
	full := s.fullOutputLocked()
	newChars := s.evictedChars + int64(len(full)) - snapshotChars
	if newChars <= 0 {
		return ""
	}
	if newChars >= int64(len(full)) {
		return full
	}
	return full[len(full)-int(newChars):]
}

func splitOutputLines(output string) []string {
	if output == "" {
		return nil
	}
	lines := []string{}
	start := 0
	for i := 0; i < len(output); i++ {
		if output[i] == '\n' {
			lines = append(lines, output[start:i])
			start = i + 1
		}
	}
	if start < len(output) {
		lines = append(lines, output[start:])
	}
	return lines
}

func (m *Manager) ResizePTY(pid, rows, cols int) (PTYSizeResult, error) {
	if rows < 1 || rows > 65535 || cols < 1 || cols > 65535 {
		return PTYSizeResult{}, errors.New("rows and cols must be between 1 and 65535")
	}
	s, err := m.get(pid)
	if err != nil {
		return PTYSizeResult{}, err
	}
	s.mu.Lock()
	if !s.usePTY || s.ptyFile == nil {
		s.mu.Unlock()
		return PTYSizeResult{}, fmt.Errorf("process %d is not a PTY session", pid)
	}
	if s.exitCode != nil {
		s.mu.Unlock()
		return PTYSizeResult{}, ErrProcessExited
	}
	file := s.ptyFile
	if err := pty.Setsize(file, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		s.mu.Unlock()
		return PTYSizeResult{}, fmt.Errorf("resize PTY for process %d: %w", pid, err)
	}
	s.mu.Unlock()
	return PTYSizeResult{PID: pid, Rows: rows, Cols: cols}, nil
}

func (m *Manager) ForceTerminate(pid int) error {
	s, err := m.get(pid)
	if err != nil {
		return err
	}
	s.mu.Lock()
	terminal := s.exitCode != nil
	s.mu.Unlock()
	// Completed sessions are retained for output/history. Their numeric PID may
	// already have been reused by Linux, so an idempotent terminate of a
	// terminal managed handle must never reach kill(2).
	if terminal {
		return nil
	}
	s.markStopping()
	if err := m.signalGroup(pid, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("interrupt process %d: %w", pid, err)
	}
	select {
	case <-s.done:
		return nil
	case <-time.After(terminateGrace):
	}
	if err := m.signalGroup(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill process %d: %w", pid, err)
	}
	select {
	case <-s.done:
		return nil
	case <-time.After(terminateGrace):
		return fmt.Errorf("process %d did not terminate", pid)
	}
}

func signalProcessGroup(pid int, signal syscall.Signal) error {
	if err := syscall.Kill(-pid, signal); err == nil {
		return nil
	}
	return syscall.Kill(pid, signal)
}

func (m *Manager) Close() error {
	m.cancelAllBatches()
	m.mu.RLock()
	pids := make([]int, 0, len(m.sessions))
	for pid, s := range m.sessions {
		s.mu.Lock()
		running := s.exitCode == nil
		s.mu.Unlock()
		if running {
			pids = append(pids, pid)
		}
	}
	m.mu.RUnlock()
	for _, pid := range pids {
		_ = m.ForceTerminate(pid)
	}
	return nil
}
