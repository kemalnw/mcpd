package workflow

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var safeHandle = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var ErrRevisionConflict = errors.New("workflow run revision conflict")

type Store struct {
	// mu protects store-wide coordination files such as leases. Run metadata
	// and job logs use narrower locks so unrelated workflows never serialize
	// behind a large log read or checkpoint.
	mu       sync.Mutex
	runLocks *keyedRWLockTable
	logLocks *keyedRWLockTable
	root     string
	now      func() time.Time
}

type CreateRequest struct {
	Title           string
	Objective       string
	SuccessCriteria []string
}

func Open(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("workflow state directory is required")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workflow state directory: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create workflow state directory: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("protect workflow state directory: %w", err)
	}
	return &Store{root: root, now: func() time.Time { return time.Now().UTC() }, runLocks: newKeyedRWLockTable(), logLocks: newKeyedRWLockTable()}, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) Create(req CreateRequest) (Run, error) {
	if strings.TrimSpace(req.Title) == "" {
		return Run{}, errors.New("run title is required")
	}
	id, err := newRunID()
	if err != nil {
		return Run{}, err
	}
	now := s.now()
	run := Run{
		SchemaVersion:    SchemaVersion,
		ID:               id,
		Revision:         1,
		Title:            strings.TrimSpace(req.Title),
		Objective:        strings.TrimSpace(req.Objective),
		SuccessCriteria:  append([]string(nil), req.SuccessCriteria...),
		State:            RunPlanned,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastCheckpointAt: now,
	}
	lock, releaseLock := s.runLocks.Acquire(id)
	lock.Lock()
	err = s.writeRunLocked(run)
	lock.Unlock()
	releaseLock()
	if err != nil {
		return Run{}, err
	}
	return cloneRun(run), nil
}

func (s *Store) Get(id string) (Run, error) {
	if err := validateHandle("run_id", id); err != nil {
		return Run{}, err
	}
	lock, releaseLock := s.runLocks.Acquire(id)
	defer releaseLock()
	lock.RLock()
	defer lock.RUnlock()
	return s.readRunLocked(id)
}

func (s *Store) List() ([]Run, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("list workflow state: %w", err)
	}
	runs := make([]Run, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !safeHandle.MatchString(entry.Name()) || !strings.HasPrefix(entry.Name(), "run_") {
			continue
		}
		lock, releaseLock := s.runLocks.Acquire(entry.Name())
		lock.RLock()
		run, err := s.readRunLocked(entry.Name())
		lock.RUnlock()
		releaseLock()
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].UpdatedAt.After(runs[j].UpdatedAt) })
	return runs, nil
}

// Update applies one atomic revisioned checkpoint. expectedRevision must match
// the stored revision; pass the revision returned by Get/Create.
func (s *Store) Update(id string, expectedRevision uint64, mutate func(*Run) error) (Run, error) {
	if mutate == nil {
		return Run{}, errors.New("workflow update function is required")
	}
	if err := validateHandle("run_id", id); err != nil {
		return Run{}, err
	}
	lock, releaseLock := s.runLocks.Acquire(id)
	defer releaseLock()
	lock.Lock()
	defer lock.Unlock()
	run, err := s.readRunLocked(id)
	if err != nil {
		return Run{}, err
	}
	if expectedRevision == 0 || run.Revision != expectedRevision {
		return Run{}, fmt.Errorf("%w: run %s is at revision %d, expected %d", ErrRevisionConflict, id, run.Revision, expectedRevision)
	}
	originalID, originalCreated := run.ID, run.CreatedAt
	if err := mutate(&run); err != nil {
		return Run{}, err
	}
	if run.ID != originalID || !run.CreatedAt.Equal(originalCreated) || run.SchemaVersion != SchemaVersion {
		return Run{}, errors.New("immutable workflow run identity/schema fields were modified")
	}
	run.Revision++
	run.UpdatedAt = s.now()
	if err := s.writeRunLocked(run); err != nil {
		return Run{}, err
	}
	return cloneRun(run), nil
}

func (s *Store) AppendJobLog(runID, jobID string, data []byte) error {
	if err := validateHandle("run_id", runID); err != nil {
		return err
	}
	if err := validateHandle("job_id", jobID); err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	runLock, releaseRunLock := s.runLocks.Acquire(runID)
	runLock.RLock()
	_, runErr := s.readRunLocked(runID)
	runLock.RUnlock()
	releaseRunLock()
	if runErr != nil {
		return runErr
	}
	logLock, releaseLogLock := s.logLocks.Acquire(runID + "/" + jobID)
	defer releaseLogLock()
	logLock.Lock()
	defer logLock.Unlock()
	logDir := filepath.Join(s.root, runID, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fmt.Errorf("create workflow log directory: %w", err)
	}
	path := filepath.Join(logDir, jobID+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open workflow job log: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("append workflow job log: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync workflow job log: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close workflow job log: %w", err)
	}
	return nil
}

func (s *Store) ReadJobLogTail(runID, jobID string, maxLines int) ([]string, error) {
	if maxLines <= 0 {
		return nil, errors.New("maxLines must be positive")
	}
	if err := validateHandle("run_id", runID); err != nil {
		return nil, err
	}
	if err := validateHandle("job_id", jobID); err != nil {
		return nil, err
	}
	logLock, releaseLogLock := s.logLocks.Acquire(runID + "/" + jobID)
	defer releaseLogLock()
	logLock.RLock()
	defer logLock.RUnlock()
	path := filepath.Join(s.root, runID, "logs", jobID+".log")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open workflow job log: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat workflow job log: %w", err)
	}
	lines, err := readLastLinesAt(f, info.Size(), maxLines)
	if err != nil {
		return nil, fmt.Errorf("read workflow job log tail: %w", err)
	}
	return lines, nil
}

const (
	workflowTailBlockBytes = 64 << 10
	workflowTailMaxBytes   = 8 << 20
)

type keyedRWLockEntry struct {
	lock sync.RWMutex
	refs int
}

type keyedRWLockTable struct {
	mu         sync.Mutex
	entries    map[string]*keyedRWLockEntry
	violations int
}

func newKeyedRWLockTable() *keyedRWLockTable {
	return &keyedRWLockTable{entries: make(map[string]*keyedRWLockEntry)}
}

// Acquire pins a keyed lock until the returned release function is called.
// The registry deletes unused entries, so long-horizon runs/jobs do not create
// an unbounded in-memory lock map. Call release only after unlocking the RWMutex.
func (t *keyedRWLockTable) Acquire(key string) (*sync.RWMutex, func()) {
	t.mu.Lock()
	entry := t.entries[key]
	if entry == nil {
		entry = &keyedRWLockEntry{}
		t.entries[key] = entry
	}
	entry.refs++
	t.mu.Unlock()
	released := false
	return &entry.lock, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		if released {
			// Keep release idempotent in production so an internal bookkeeping bug
			// cannot corrupt the live lock table. Tests assert violations remain zero.
			t.violations++
			return
		}
		released = true
		entry.refs--
		if entry.refs == 0 && t.entries[key] == entry {
			delete(t.entries, key)
		}
	}
}

func (t *keyedRWLockTable) InvariantViolations() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.violations
}

func (t *keyedRWLockTable) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

func readLastLinesAt(r io.ReaderAt, size int64, maxLines int) ([]string, error) {
	if size <= 0 {
		return nil, nil
	}
	pos := size
	var chunks [][]byte
	readBytes := 0
	newlines := 0
	for pos > 0 && newlines <= maxLines {
		block := int64(workflowTailBlockBytes)
		if block > pos {
			block = pos
		}
		if readBytes+int(block) > workflowTailMaxBytes {
			return nil, fmt.Errorf("requested tail exceeds %d-byte scan budget; log contains an oversized line or too few line breaks", workflowTailMaxBytes)
		}
		pos -= block
		buf := make([]byte, int(block))
		n, err := r.ReadAt(buf, pos)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		buf = buf[:n]
		chunks = append(chunks, buf)
		readBytes += n
		newlines += bytes.Count(buf, []byte{'\n'})
	}
	data := make([]byte, 0, readBytes)
	for i := len(chunks) - 1; i >= 0; i-- {
		data = append(data, chunks[i]...)
	}
	parts := strings.Split(string(data), "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > maxLines {
		parts = parts[len(parts)-maxLines:]
	}
	return parts, nil
}

func (s *Store) readRunLocked(id string) (Run, error) {
	path := filepath.Join(s.root, id, "run.json")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Run{}, fmt.Errorf("workflow run %s not found", id)
		}
		return Run{}, fmt.Errorf("open workflow run %s: %w", id, err)
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 4<<20))
	dec.DisallowUnknownFields()
	var run Run
	if err := dec.Decode(&run); err != nil {
		return Run{}, fmt.Errorf("decode workflow run %s: %w", id, err)
	}
	if run.SchemaVersion != SchemaVersion || run.ID != id || run.Revision == 0 {
		return Run{}, fmt.Errorf("workflow run %s has invalid schema/identity/revision", id)
	}
	return cloneRun(run), nil
}

func (s *Store) writeRunLocked(run Run) error {
	if err := validateHandle("run_id", run.ID); err != nil {
		return err
	}
	dir := filepath.Join(s.root, run.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create workflow run directory: %w", err)
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workflow run: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".run-*.tmp")
	if err != nil {
		return fmt.Errorf("create workflow run temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	defer cleanup()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect workflow run temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write workflow run temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync workflow run temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close workflow run temp file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(dir, "run.json")); err != nil {
		return fmt.Errorf("replace workflow run: %w", err)
	}
	d, err := os.Open(dir)
	if err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

func validateHandle(field, value string) error {
	if !safeHandle.MatchString(value) {
		return fmt.Errorf("%s contains unsafe characters", field)
	}
	return nil
}

func newRunID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate workflow run id: %w", err)
	}
	return "run_" + hex.EncodeToString(b[:]), nil
}

func cloneRun(run Run) Run {
	run.SuccessCriteria = append([]string(nil), run.SuccessCriteria...)
	run.NextActions = append([]string(nil), run.NextActions...)
	run.Items = append([]WorkItem(nil), run.Items...)
	for i := range run.Items {
		run.Items[i].DependsOn = append([]string(nil), run.Items[i].DependsOn...)
	}
	if run.Handoff != nil {
		handoff := *run.Handoff
		handoff.Blockers = append([]string(nil), handoff.Blockers...)
		handoff.Evidence = append([]EvidenceReference(nil), handoff.Evidence...)
		handoff.ActiveHandles = append([]ActiveHandle(nil), handoff.ActiveHandles...)
		handoff.ActiveSideEffects = append([]string(nil), handoff.ActiveSideEffects...)
		handoff.PendingApprovals = append([]string(nil), handoff.PendingApprovals...)
		handoff.DoNotRepeat = append([]string(nil), handoff.DoNotRepeat...)
		handoff.CleanupState = append([]string(nil), handoff.CleanupState...)
		handoff.Recommendations = append([]Recommendation(nil), handoff.Recommendations...)
		handoff.NextActions = append([]string(nil), handoff.NextActions...)
		run.Handoff = &handoff
	}
	return run
}
