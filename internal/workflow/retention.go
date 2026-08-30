package workflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type GCPolicy struct {
	CompletedRetention time.Duration
	DryRun             bool
	MaxDeletes         int
}

type GCResult struct {
	ScannedRuns              int      `json:"scanned_runs"`
	EligibleRuns             int      `json:"eligible_runs"`
	DeletedRuns              int      `json:"deleted_runs"`
	DeletedBytes             int64    `json:"deleted_bytes"`
	SkippedActiveRuns        int      `json:"skipped_active_runs"`
	SkippedLeasedRuns        int      `json:"skipped_leased_runs"`
	PrunedIdempotencyRecords int      `json:"pruned_idempotency_records"`
	PrunedExpiredLeases      int      `json:"pruned_expired_leases"`
	CleanedTrashEntries      int      `json:"cleaned_trash_entries"`
	RunIDs                   []string `json:"run_ids,omitempty"`
	DryRun                   bool     `json:"dry_run"`
}

const defaultGCMaxDeletes = 100

func (s *Store) CollectGarbage(policy GCPolicy) (GCResult, error) {
	if policy.CompletedRetention <= 0 {
		return GCResult{}, errors.New("completed retention must be positive")
	}
	if policy.MaxDeletes == 0 {
		policy.MaxDeletes = defaultGCMaxDeletes
	}
	if policy.MaxDeletes < 1 || policy.MaxDeletes > 1000 {
		return GCResult{}, errors.New("max deletes must be between 1 and 1000")
	}
	result := GCResult{DryRun: policy.DryRun}
	now := s.now()

	// Coordination metadata (leases/idempotency) uses the store mutex. Prune
	// expired entries before evaluating run eligibility so stale ownership never
	// protects data forever.
	s.mu.Lock()
	leaseState, err := s.readLeasesLocked()
	if err != nil {
		s.mu.Unlock()
		return result, err
	}
	active := activeLeases(leaseState.Leases, now)
	result.PrunedExpiredLeases = len(leaseState.Leases) - len(active)
	if result.PrunedExpiredLeases > 0 && !policy.DryRun {
		leaseState.Leases = active
		if err := s.writeLeasesLocked(leaseState); err != nil {
			s.mu.Unlock()
			return result, err
		}
	}
	if !policy.DryRun {
		count, err := s.pruneExpiredIdempotencyLocked(now)
		if err != nil {
			s.mu.Unlock()
			return result, err
		}
		result.PrunedIdempotencyRecords += count
	} else {
		count, err := s.countExpiredIdempotencyLocked(now)
		if err != nil {
			s.mu.Unlock()
			return result, err
		}
		result.PrunedIdempotencyRecords += count
	}
	s.mu.Unlock()

	entries, err := os.ReadDir(s.root)
	if err != nil {
		return result, fmt.Errorf("list workflow state for GC: %w", err)
	}
	type candidate struct {
		id      string
		updated time.Time
		bytes   int64
	}
	candidates := make([]candidate, 0)
	cutoff := now.Add(-policy.CompletedRetention)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "run_") || !safeHandle.MatchString(entry.Name()) {
			continue
		}
		result.ScannedRuns++
		run, err := s.Get(entry.Name())
		if err != nil {
			return result, err
		}
		if !terminalRunState(run.State) {
			result.SkippedActiveRuns++
			continue
		}
		if run.UpdatedAt.After(cutoff) {
			continue
		}
		if runHasActiveLease(active, run.ID) {
			result.SkippedLeasedRuns++
			continue
		}
		bytes, err := directoryBytes(filepath.Join(s.root, run.ID))
		if err != nil {
			return result, err
		}
		candidates = append(candidates, candidate{id: run.ID, updated: run.UpdatedAt, bytes: bytes})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].updated.Equal(candidates[j].updated) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].updated.Before(candidates[j].updated)
	})
	result.EligibleRuns = len(candidates)
	if len(candidates) > policy.MaxDeletes {
		candidates = candidates[:policy.MaxDeletes]
	}
	for _, item := range candidates {
		result.RunIDs = append(result.RunIDs, item.id)
		if policy.DryRun {
			continue
		}
		deleted, pruned, err := s.deleteTerminalRun(item.id, cutoff)
		if err != nil {
			return result, err
		}
		if deleted {
			result.DeletedRuns++
			result.DeletedBytes += item.bytes
			result.PrunedIdempotencyRecords += pruned
		}
	}
	if !policy.DryRun {
		cleaned, err := s.cleanTrash()
		if err != nil {
			return result, err
		}
		result.CleanedTrashEntries = cleaned
	}
	return result, nil
}

func terminalRunState(state RunState) bool {
	switch state {
	case RunCompleted, RunFailed, RunCanceled:
		return true
	default:
		return false
	}
}

func runHasActiveLease(leases []Lease, runID string) bool {
	for _, lease := range leases {
		if lease.OwnerRunID == runID {
			return true
		}
	}
	return false
}

func (s *Store) deleteTerminalRun(runID string, cutoff time.Time) (bool, int, error) {
	lock, release := s.runLocks.Acquire(runID)
	defer release()
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.readRunLocked(runID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return false, 0, nil
		}
		return false, 0, err
	}
	if !terminalRunState(run.State) || run.UpdatedAt.After(cutoff) {
		return false, 0, nil
	}
	leases, err := s.readLeasesLocked()
	if err != nil {
		return false, 0, err
	}
	active := activeLeases(leases.Leases, s.now())
	if runHasActiveLease(active, runID) {
		return false, 0, nil
	}
	pruned, err := s.removeIdempotencyForRunLocked(runID)
	if err != nil {
		return false, 0, err
	}
	trashDir := filepath.Join(s.root, ".trash")
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return false, pruned, fmt.Errorf("create workflow trash: %w", err)
	}
	source := filepath.Join(s.root, runID)
	target := filepath.Join(trashDir, runID+"-"+fmt.Sprintf("%d", s.now().UnixNano()))
	if err := os.Rename(source, target); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, pruned, nil
		}
		return false, pruned, fmt.Errorf("stage workflow run for deletion: %w", err)
	}
	if err := syncDirectory(s.root); err != nil {
		return false, pruned, err
	}
	return true, pruned, nil
}

func (s *Store) cleanTrash() (int, error) {
	dir := filepath.Join(s.root, ".trash")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("list workflow trash: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return count, fmt.Errorf("remove workflow trash entry: %w", err)
		}
		count++
	}
	return count, nil
}

func directoryBytes(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("measure workflow run: %w", err)
	}
	return total, nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open workflow directory for sync: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync workflow directory: %w", err)
	}
	return nil
}
