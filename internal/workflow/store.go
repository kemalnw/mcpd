package workflow

import (
	"bufio"
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
	mu   sync.Mutex
	root string
	now  func() time.Time
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
	return &Store{root: root, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Store) Root() string { return s.root }

func (s *Store) Create(req CreateRequest) (Run, error) {
	if strings.TrimSpace(req.Title) == "" {
		return Run{}, errors.New("run title is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := newRunID()
	if err != nil {
		return Run{}, err
	}
	now := s.now()
	run := Run{
		SchemaVersion:   SchemaVersion,
		ID:              id,
		Revision:        1,
		Title:           strings.TrimSpace(req.Title),
		Objective:       strings.TrimSpace(req.Objective),
		SuccessCriteria: append([]string(nil), req.SuccessCriteria...),
		State:           RunPlanned,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.writeRunLocked(run); err != nil {
		return Run{}, err
	}
	return cloneRun(run), nil
}

func (s *Store) Get(id string) (Run, error) {
	if err := validateHandle("run_id", id); err != nil {
		return Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readRunLocked(id)
}

func (s *Store) List() ([]Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("list workflow state: %w", err)
	}
	runs := make([]Run, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !safeHandle.MatchString(entry.Name()) || !strings.HasPrefix(entry.Name(), "run_") {
			continue
		}
		run, err := s.readRunLocked(entry.Name())
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
	s.mu.Lock()
	defer s.mu.Unlock()
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readRunLocked(runID); err != nil {
		return err
	}
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
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.root, runID, "logs", jobID+".log")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open workflow job log: %w", err)
	}
	defer f.Close()
	lines := make([]string, 0, maxLines)
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 64<<10)
	scanner.Buffer(buf, 2<<20)
	for scanner.Scan() {
		if len(lines) == maxLines {
			copy(lines, lines[1:])
			lines[len(lines)-1] = scanner.Text()
		} else {
			lines = append(lines, scanner.Text())
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read workflow job log: %w", err)
	}
	return lines, nil
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
	return run
}
