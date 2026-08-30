package search

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxResults  = 1000
	defaultRetention   = 5 * time.Minute
	defaultInitialWait = 40 * time.Millisecond
)

type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*session
	opts     ManagerOptions
	rgPath   string
	stop     chan struct{}
	done     chan struct{}
}

func NewManager(opts ManagerOptions) (*Manager, error) {
	if opts.DefaultMaxResults < 0 || opts.Retention < 0 || opts.InitialWait < 0 {
		return nil, errors.New("invalid search manager limits")
	}
	if opts.DefaultMaxResults == 0 {
		opts.DefaultMaxResults = defaultMaxResults
	}
	if opts.Retention == 0 {
		opts.Retention = defaultRetention
	}
	if opts.InitialWait == 0 {
		opts.InitialWait = defaultInitialWait
	}
	preferredRoots := make([]string, 0, len(opts.PreferredRoots))
	for _, root := range opts.PreferredRoots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve preferred search root %q: %w", root, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat preferred search root %q: %w", abs, err)
		}
		if info.IsDir() {
			preferredRoots = append(preferredRoots, filepath.Clean(abs))
		}
	}
	opts.PreferredRoots = preferredRoots
	m := &Manager{sessions: make(map[string]*session), opts: opts, stop: make(chan struct{}), done: make(chan struct{})}
	if !opts.DisableRipgrep {
		if opts.RipgrepPath != "" {
			if _, err := os.Stat(opts.RipgrepPath); err != nil {
				return nil, fmt.Errorf("ripgrep path %q: %w", opts.RipgrepPath, err)
			}
			m.rgPath = opts.RipgrepPath
		} else if path, err := exec.LookPath("rg"); err == nil {
			m.rgPath = path
		}
	}
	go m.cleanupLoop()
	return m, nil
}

func (m *Manager) Close() error {
	select {
	case <-m.stop:
		return nil
	default:
		close(m.stop)
	}
	m.mu.RLock()
	dones := make([]<-chan struct{}, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.cancel()
		dones = append(dones, s.done)
	}
	m.mu.RUnlock()
	for _, done := range dones {
		<-done
	}
	<-m.done
	return nil
}

func (m *Manager) Start(ctx context.Context, opts Options) (StartResult, error) {
	if err := m.normalizeAndValidate(&opts); err != nil {
		return StartResult{}, err
	}
	backend := "native"
	if m.rgPath != "" {
		backend = "ripgrep"
	}
	searchCtx, cancel := context.WithCancel(context.Background())
	id := newSessionID()
	s := newSession(id, opts, backend, cancel)
	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	go m.run(searchCtx, s)

	wait := time.NewTimer(m.opts.InitialWait)
	defer wait.Stop()
	select {
	case <-ctx.Done():
		s.cancel()
		return StartResult{}, ctx.Err()
	case <-s.done:
	case <-wait.C:
	}
	read := s.snapshot(0, lenSnapshot(s))
	return StartResult{SessionID: id, IsComplete: read.IsComplete, IsError: read.IsError, Results: read.Results,
		TotalResults: read.TotalResults, TotalMatches: read.TotalMatches, RuntimeMS: read.RuntimeMS, Backend: backend}, nil
}

func (m *Manager) Read(sessionID string, offset, length int) (ReadResult, error) {
	s := m.get(sessionID)
	if s == nil {
		return ReadResult{}, fmt.Errorf("search session %s not found", sessionID)
	}
	return s.snapshot(offset, length), nil
}

func (m *Manager) Stop(sessionID string) StopResult {
	s := m.get(sessionID)
	if s == nil {
		return StopResult{SessionID: sessionID, Stopped: false}
	}
	s.mu.RLock()
	complete := s.complete
	s.mu.RUnlock()
	if complete {
		return StopResult{SessionID: sessionID, Stopped: false}
	}
	s.cancel()
	return StopResult{SessionID: sessionID, Stopped: true}
}

func (m *Manager) List() []SessionInfo {
	m.mu.RLock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.info())
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

func (m *Manager) get(id string) *session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

func (m *Manager) normalizeAndValidate(opts *Options) error {
	if strings.TrimSpace(opts.RootPath) == "" || strings.TrimSpace(opts.Pattern) == "" {
		return errors.New("path and pattern are required")
	}
	if opts.SearchType == "" {
		opts.SearchType = TypeFiles
	}
	if opts.SearchType != TypeFiles && opts.SearchType != TypeContent {
		return fmt.Errorf("searchType must be files or content")
	}
	abs, err := filepath.Abs(opts.RootPath)
	if err != nil {
		return fmt.Errorf("resolve search path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("stat search path %q: %w", abs, err)
	}
	if !info.IsDir() && opts.SearchType == TypeFiles {
		return fmt.Errorf("file search path %q must be a directory", abs)
	}
	opts.RootPath = abs
	if preferred, ok := m.resolvePreferredRoot(*opts); ok {
		opts.RootPath = preferred
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = m.opts.DefaultMaxResults
	}
	if opts.ContextLines < 0 || opts.TimeoutMS < 0 {
		return errors.New("contextLines and timeout_ms must be non-negative")
	}
	if opts.TimeoutMS == 0 && opts.SearchType == TypeFiles && isExactFilename(opts.Pattern) {
		opts.TimeoutMS = 1500
	}
	if opts.SearchType == TypeContent && !opts.LiteralSearch {
		pattern := opts.Pattern
		if opts.IgnoreCase {
			pattern = "(?i)" + pattern
		}
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("compile search pattern: %w", err)
		}
	}
	return nil
}

func (m *Manager) run(ctx context.Context, s *session) {
	stopped := false
	var err error
	incomplete := false
	if s.opts.TimeoutMS > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(s.opts.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	if m.rgPath != "" {
		incomplete, err = m.runRipgrep(ctx, s)
	} else {
		incomplete, err = m.runNative(ctx, s)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		stopped = true
		err = nil
	} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		stopped = true
		err = nil
	}
	s.finish(err, incomplete, stopped)
}

func (m *Manager) cleanupLoop() {
	defer close(m.done)
	interval := m.opts.Retention / 2
	if interval > time.Minute {
		interval = time.Minute
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-ticker.C:
			m.cleanup(now)
		}
	}
}

func (m *Manager) cleanup(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		s.mu.RLock()
		complete := s.complete
		lastRead := s.lastReadTime
		s.mu.RUnlock()
		if complete && now.Sub(lastRead) >= m.opts.Retention {
			delete(m.sessions, id)
		}
	}
}

const preferredSearchMaxDepth = 4

var preferredSearchSkipDirs = map[string]struct{}{
	".git": {}, ".hg": {}, ".svn": {}, "node_modules": {}, "vendor": {},
}

func (m *Manager) resolvePreferredRoot(opts Options) (string, bool) {
	if len(m.opts.PreferredRoots) == 0 {
		return "", false
	}
	requested := filepath.Clean(opts.RootPath)
	hint := strings.TrimSpace(opts.PathHint)
	if hint != "" {
		for _, root := range m.opts.PreferredRoots {
			if !pathWithin(requested, root) {
				continue
			}
			if dir := findPreferredHintDir(root, hint, opts.IncludeHidden); dir != "" {
				return dir, true
			}
		}
	}

	if opts.SearchType != TypeFiles || !opts.EarlyTermination || !isExactFilename(opts.Pattern) || filepath.Base(opts.Pattern) != opts.Pattern {
		return "", false
	}
	direct := filepath.Join(requested, opts.Pattern)
	if isMatchingRegularFile(direct, opts) && matchesFilePatterns(filepath.Base(direct), opts.FilePattern, opts.IgnoreCase) {
		return "", false
	}

	var fallback string
	for _, root := range m.opts.PreferredRoots {
		if !pathWithin(requested, root) {
			continue
		}
		_, first := findPreferredExactFile(root, opts, "")
		if fallback == "" && first != "" {
			fallback = first
		}
	}
	if fallback != "" {
		return filepath.Dir(fallback), true
	}
	return "", false
}

func findPreferredHintDir(root, hint string, includeHidden bool) string {
	root = filepath.Clean(root)
	var found string
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				return nil
			}
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		depth := strings.Count(filepath.ToSlash(rel), "/") + 1
		if depth > preferredSearchMaxDepth {
			return filepath.SkipDir
		}
		if _, skip := preferredSearchSkipDirs[entry.Name()]; skip {
			return filepath.SkipDir
		}
		if !includeHidden && strings.HasPrefix(entry.Name(), ".") {
			return filepath.SkipDir
		}
		if pathMatchesHint(rel, hint) {
			found = path
			return errStopSearch
		}
		return nil
	})
	return found
}

func findPreferredExactFile(root string, opts Options, hint string) (hinted, first string) {
	root = filepath.Clean(root)
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				return nil
			}
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		depth := strings.Count(filepath.ToSlash(rel), "/") + 1
		if entry.IsDir() {
			if depth > preferredSearchMaxDepth {
				return filepath.SkipDir
			}
			if _, skip := preferredSearchSkipDirs[entry.Name()]; skip {
				return filepath.SkipDir
			}
			if !opts.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if depth > preferredSearchMaxDepth || (!opts.IncludeHidden && strings.HasPrefix(entry.Name(), ".")) {
			return nil
		}
		if !isMatchingRegularFile(path, opts) || !matchesFilePatterns(rel, opts.FilePattern, opts.IgnoreCase) {
			return nil
		}
		if first == "" {
			first = path
		}
		if hint != "" && pathMatchesHint(rel, hint) {
			hinted = path
			return errStopSearch
		}
		return nil
	})
	return hinted, first
}

func isMatchingRegularFile(path string, opts Options) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return exactFilenameMatch(path, opts.Pattern, opts.IgnoreCase)
}

func pathMatchesHint(path, hint string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	hint = strings.ToLower(strings.TrimSpace(filepath.ToSlash(hint)))
	if hint == "" {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == hint {
			return true
		}
	}
	return strings.Contains(path, hint)
}

func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func newSessionID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "search_" + hex.EncodeToString(b[:]) + "_" + fmt.Sprint(time.Now().UnixMilli())
	}
	return fmt.Sprintf("search_%d", time.Now().UnixNano())
}

func lenSnapshot(s *session) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.results)
}
