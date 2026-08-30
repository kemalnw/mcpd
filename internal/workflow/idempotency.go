package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	idempotencySchemaVersion     = 1
	durableIdempotencyTTL        = 30 * 24 * time.Hour
	maxDurableIdempotencyRecords = 10_000
	maxIdempotencyKeyBytes       = 512
)

var ErrIdempotencyConflict = errors.New("idempotency key conflicts with a different request")

type idempotencyRecord struct {
	SchemaVersion int       `json:"schema_version"`
	KeyHash       string    `json:"key_hash"`
	RequestHash   string    `json:"request_hash"`
	RunID         string    `json:"run_id"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// CreateIdempotent creates one durable run for an idempotency key. A retry with
// the same normalized request returns the original run; conflicting reuse is
// rejected. Only a SHA-256 digest of the caller key is persisted.
func (s *Store) CreateIdempotent(req CreateRequest, key string) (Run, bool, error) {
	key = strings.TrimSpace(key)
	if key == "" || len(key) > maxIdempotencyKeyBytes || strings.ContainsRune(key, '\x00') {
		return Run{}, false, errors.New("idempotency_key must be 1..512 bytes and contain no NUL")
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Objective = strings.TrimSpace(req.Objective)
	if req.Title == "" {
		return Run{}, false, errors.New("run title is required")
	}
	requestHash, err := createRequestHash(req)
	if err != nil {
		return Run{}, false, err
	}
	keyHash := sha256Hex([]byte(key))

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if err := s.ensureIdempotencyCapacityLocked(now); err != nil {
		return Run{}, false, err
	}
	recordPath := s.idempotencyRecordPath(keyHash)
	record, err := readIdempotencyRecord(recordPath)
	if err == nil {
		if !record.ExpiresAt.After(now) {
			if err := os.Remove(recordPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return Run{}, false, fmt.Errorf("remove expired idempotency record: %w", err)
			}
		} else {
			if record.KeyHash != keyHash || record.RequestHash != requestHash {
				return Run{}, false, ErrIdempotencyConflict
			}
			run, readErr := s.readRunLocked(record.RunID)
			if readErr == nil {
				return run, true, nil
			}
			// A crash can occur after the durable idempotency record is synced but
			// before run.json is installed. Repair that partial transaction using
			// the already-reserved run ID instead of creating a duplicate run.
			if !strings.Contains(readErr.Error(), "not found") {
				return Run{}, false, readErr
			}
			run = newRunForID(req, record.RunID, record.CreatedAt)
			if err := s.writeRunLocked(run); err != nil {
				return Run{}, false, err
			}
			return cloneRun(run), true, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Run{}, false, err
	}

	runID, err := newRunID()
	if err != nil {
		return Run{}, false, err
	}
	record = idempotencyRecord{
		SchemaVersion: idempotencySchemaVersion,
		KeyHash:       keyHash,
		RequestHash:   requestHash,
		RunID:         runID,
		CreatedAt:     now,
		ExpiresAt:     now.Add(durableIdempotencyTTL),
	}
	if err := s.writeIdempotencyRecordLocked(recordPath, record); err != nil {
		return Run{}, false, err
	}
	run := newRunForID(req, runID, now)
	if err := s.writeRunLocked(run); err != nil {
		return Run{}, false, err
	}
	return cloneRun(run), false, nil
}

func newRunForID(req CreateRequest, id string, now time.Time) Run {
	return Run{
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
}

func createRequestHash(req CreateRequest) (string, error) {
	payload := struct {
		Title           string   `json:"title"`
		Objective       string   `json:"objective"`
		SuccessCriteria []string `json:"success_criteria"`
	}{Title: strings.TrimSpace(req.Title), Objective: strings.TrimSpace(req.Objective), SuccessCriteria: append([]string(nil), req.SuccessCriteria...)}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode idempotent create request: %w", err)
	}
	return sha256Hex(data), nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Store) idempotencyRecordPath(keyHash string) string {
	return filepath.Join(s.root, "idempotency", keyHash+".json")
}

func readIdempotencyRecord(path string) (idempotencyRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return idempotencyRecord{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 64<<10))
	dec.DisallowUnknownFields()
	var record idempotencyRecord
	if err := dec.Decode(&record); err != nil {
		return idempotencyRecord{}, fmt.Errorf("decode idempotency record: %w", err)
	}
	if record.SchemaVersion != idempotencySchemaVersion || record.KeyHash == "" || record.RequestHash == "" || record.RunID == "" || record.CreatedAt.IsZero() || record.ExpiresAt.IsZero() {
		return idempotencyRecord{}, errors.New("invalid idempotency record")
	}
	return record, nil
}

func (s *Store) writeIdempotencyRecordLocked(path string, record idempotencyRecord) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create idempotency directory: %w", err)
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode idempotency record: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".idempotency-*.tmp")
	if err != nil {
		return fmt.Errorf("create idempotency temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect idempotency temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write idempotency temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync idempotency temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close idempotency temp file: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace idempotency record: %w", err)
	}
	return nil
}

func (s *Store) ensureIdempotencyCapacityLocked(now time.Time) error {
	dir := filepath.Join(s.root, "idempotency")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list idempotency records: %w", err)
	}
	if len(entries) < maxDurableIdempotencyRecords {
		return nil
	}
	type candidate struct {
		path      string
		expiresAt time.Time
	}
	candidates := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		record, readErr := readIdempotencyRecord(path)
		if readErr != nil {
			return readErr
		}
		if !record.ExpiresAt.After(now) {
			candidates = append(candidates, candidate{path: path, expiresAt: record.ExpiresAt})
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].expiresAt.Before(candidates[j].expiresAt) })
	for _, item := range candidates {
		if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("prune idempotency record: %w", err)
		}
	}
	remaining, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("recount idempotency records: %w", err)
	}
	if len(remaining) >= maxDurableIdempotencyRecords {
		return fmt.Errorf("durable idempotency record capacity %d reached", maxDurableIdempotencyRecords)
	}
	return nil
}

func (s *Store) pruneExpiredIdempotencyLocked(now time.Time) (int, error) {
	dir := filepath.Join(s.root, "idempotency")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("list idempotency records: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		record, err := readIdempotencyRecord(path)
		if err != nil {
			return count, err
		}
		if record.ExpiresAt.After(now) {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return count, fmt.Errorf("remove expired idempotency record: %w", err)
		}
		count++
	}
	return count, nil
}

func (s *Store) countExpiredIdempotencyLocked(now time.Time) (int, error) {
	dir := filepath.Join(s.root, "idempotency")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("list idempotency records: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := readIdempotencyRecord(filepath.Join(dir, entry.Name()))
		if err != nil {
			return count, err
		}
		if !record.ExpiresAt.After(now) {
			count++
		}
	}
	return count, nil
}

func (s *Store) removeIdempotencyForRunLocked(runID string) (int, error) {
	dir := filepath.Join(s.root, "idempotency")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("list idempotency records: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		record, err := readIdempotencyRecord(path)
		if err != nil {
			return count, err
		}
		if record.RunID != runID {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return count, fmt.Errorf("remove run idempotency record: %w", err)
		}
		count++
	}
	return count, nil
}
