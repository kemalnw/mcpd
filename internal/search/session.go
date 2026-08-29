package search

import (
	"context"
	"sync"
	"time"
)

type session struct {
	mu sync.RWMutex

	id       string
	opts     Options
	backend  string
	results  []Result
	matches  int
	contexts int

	complete      bool
	isError       bool
	err           string
	incomplete    bool
	stopped       bool
	startTime     time.Time
	lastReadTime  time.Time
	completedTime time.Time

	cancel context.CancelFunc
	done   chan struct{}
}

func newSession(id string, opts Options, backend string, cancel context.CancelFunc) *session {
	now := time.Now()
	return &session{id: id, opts: opts, backend: backend, startTime: now, lastReadTime: now, cancel: cancel, done: make(chan struct{})}
}

func (s *session) add(result Result) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.complete {
		return false
	}
	if result.IsContext {
		s.contexts++
	} else {
		if s.opts.MaxResults > 0 && s.matches >= s.opts.MaxResults {
			return false
		}
		s.matches++
	}
	s.results = append(s.results, result)
	return true
}

func (s *session) matchLimitReached() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.opts.MaxResults > 0 && s.matches >= s.opts.MaxResults
}

func (s *session) remainingMatchSlots() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.opts.MaxResults <= 0 {
		return int(^uint(0) >> 1)
	}
	remaining := s.opts.MaxResults - s.matches
	if remaining < 0 {
		return 0
	}
	return remaining
}

func (s *session) finish(err error, incomplete, stopped bool) {
	s.mu.Lock()
	if s.complete {
		s.mu.Unlock()
		return
	}
	s.complete = true
	s.incomplete = incomplete
	s.stopped = stopped
	if err != nil {
		s.isError = true
		s.err = err.Error()
	}
	s.completedTime = time.Now()
	close(s.done)
	s.mu.Unlock()
}

func (s *session) snapshot(offset, length int) ReadResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReadTime = time.Now()

	total := len(s.results)
	var selected []Result
	if offset < 0 {
		start := total - (-offset)
		if start < 0 {
			start = 0
		}
		selected = append([]Result(nil), s.results[start:]...)
	} else {
		if offset > total {
			offset = total
		}
		if length <= 0 {
			length = 100
		}
		end := offset + length
		if end > total {
			end = total
		}
		selected = append([]Result(nil), s.results[offset:end]...)
	}

	hasMore := false
	if offset >= 0 {
		hasMore = offset+len(selected) < total || !s.complete
	}
	return ReadResult{
		SessionID: s.id, Results: selected, ReturnedCount: len(selected), TotalResults: s.matches + s.contexts,
		TotalMatches: s.matches, IsComplete: s.complete, IsError: s.isError, Error: s.err,
		HasMoreResults: hasMore, RuntimeMS: time.Since(s.startTime).Milliseconds(), WasIncomplete: s.incomplete, Backend: s.backend,
	}
}

func (s *session) info() SessionInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return SessionInfo{SessionID: s.id, SearchType: string(s.opts.SearchType), Pattern: s.opts.Pattern, IsComplete: s.complete, IsError: s.isError,
		RuntimeMS: time.Since(s.startTime).Milliseconds(), TotalResults: s.matches + s.contexts, TotalMatches: s.matches, WasIncomplete: s.incomplete, Backend: s.backend}
}
