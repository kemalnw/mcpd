package audit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const defaultRecentLimit = 1000

type Event struct {
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	Tool       string    `json:"tool"`
	Arguments  any       `json:"arguments,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	Status     string    `json:"status"`
	Error      string    `json:"error,omitempty"`
}

type ToolStats struct {
	Calls       uint64 `json:"calls"`
	Errors      uint64 `json:"errors"`
	DurationMS  int64  `json:"duration_ms"`
	LastCallUTC string `json:"last_call_utc,omitempty"`
}

type Stats struct {
	StartedAt time.Time            `json:"started_at"`
	Total     uint64               `json:"total_calls"`
	Errors    uint64               `json:"total_errors"`
	Tools     map[string]ToolStats `json:"tools"`
}

type Store struct {
	mu        sync.RWMutex
	enabled   bool
	file      *os.File
	writer    *bufio.Writer
	recent    []Event
	maxRecent int
	stats     Stats
}

func Open(enabled bool, path string) (*Store, error) {
	s := &Store{
		enabled:   enabled,
		maxRecent: defaultRecentLimit,
		stats:     Stats{StartedAt: time.Now().UTC(), Tools: make(map[string]ToolStats)},
	}
	if !enabled {
		return s, nil
	}
	if path == "" {
		return nil, errors.New("audit path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create audit directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open audit log: %w", err)
	}
	s.file = f
	s.writer = bufio.NewWriterSize(f, 64<<10)
	return s, nil
}

func (s *Store) Record(event Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.Status == "" {
		if event.Error == "" {
			event.Status = "success"
		} else {
			event.Status = "error"
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.recent = append(s.recent, event)
	if excess := len(s.recent) - s.maxRecent; excess > 0 {
		copy(s.recent, s.recent[excess:])
		s.recent = s.recent[:s.maxRecent]
	}

	s.stats.Total++
	tool := s.stats.Tools[event.Tool]
	tool.Calls++
	tool.DurationMS += event.DurationMS
	tool.LastCallUTC = event.Timestamp.Format(time.RFC3339Nano)
	if event.Error != "" {
		s.stats.Errors++
		tool.Errors++
	}
	s.stats.Tools[event.Tool] = tool

	if !s.enabled {
		return nil
	}
	if err := json.NewEncoder(s.writer).Encode(event); err != nil {
		return fmt.Errorf("encode audit event: %w", err)
	}
	if err := s.writer.Flush(); err != nil {
		return fmt.Errorf("flush audit event: %w", err)
	}
	return nil
}

func (s *Store) Recent(limit int) []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.recent) {
		limit = len(s.recent)
	}
	start := len(s.recent) - limit
	out := make([]Event, limit)
	copy(out, s.recent[start:])
	return out
}

func (s *Store) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Stats{StartedAt: s.stats.StartedAt, Total: s.stats.Total, Errors: s.stats.Errors, Tools: make(map[string]ToolStats, len(s.stats.Tools))}
	for name, stat := range s.stats.Tools {
		out.Tools[name] = stat
	}
	return out
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer != nil {
		if err := s.writer.Flush(); err != nil {
			return err
		}
	}
	if s.file != nil {
		return s.file.Close()
	}
	return nil
}
