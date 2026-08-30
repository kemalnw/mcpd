package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const leaseSchemaVersion = 1

type LeaseKind string

const (
	LeaseNamed LeaseKind = "named"
	LeasePath  LeaseKind = "path"
)

type Lease struct {
	Kind         LeaseKind `json:"kind"`
	Resource     string    `json:"resource"`
	OwnerRunID   string    `json:"owner_run_id"`
	OwnerJobID   string    `json:"owner_job_id,omitempty"`
	FencingToken uint64    `json:"fencing_token"`
	AcquiredAt   time.Time `json:"acquired_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type LeaseRequest struct {
	Kind       LeaseKind
	Resource   string
	OwnerRunID string
	OwnerJobID string
	TTL        time.Duration
}

type leaseFile struct {
	SchemaVersion    int     `json:"schema_version"`
	NextFencingToken uint64  `json:"next_fencing_token"`
	Leases           []Lease `json:"leases"`
}

var ErrLeaseConflict = errors.New("resource lease conflict")

func (s *Store) AcquireLease(req LeaseRequest) (Lease, error) {
	if err := validateHandle("owner_run_id", req.OwnerRunID); err != nil {
		return Lease{}, err
	}
	if req.OwnerJobID != "" {
		if err := validateHandle("owner_job_id", req.OwnerJobID); err != nil {
			return Lease{}, err
		}
	}
	resource, err := normalizeLeaseResource(req.Kind, req.Resource)
	if err != nil {
		return Lease{}, err
	}
	if req.TTL <= 0 || req.TTL > 24*time.Hour {
		return Lease{}, errors.New("lease TTL must be > 0 and <= 24h")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readRunLocked(req.OwnerRunID); err != nil {
		return Lease{}, err
	}
	state, err := s.readLeasesLocked()
	if err != nil {
		return Lease{}, err
	}
	now := s.now()
	state.Leases = activeLeases(state.Leases, now)
	for _, existing := range state.Leases {
		if !leasesConflict(existing, Lease{Kind: req.Kind, Resource: resource}) {
			continue
		}
		if existing.OwnerRunID == req.OwnerRunID && existing.OwnerJobID == req.OwnerJobID && existing.Kind == req.Kind && existing.Resource == resource {
			return existing, nil
		}
		return Lease{}, fmt.Errorf("%w: %s %q is held by run %s job %s until %s", ErrLeaseConflict, existing.Kind, existing.Resource, existing.OwnerRunID, existing.OwnerJobID, existing.ExpiresAt.Format(time.RFC3339))
	}
	state.NextFencingToken++
	if state.NextFencingToken == 0 {
		return Lease{}, errors.New("lease fencing token exhausted")
	}
	lease := Lease{Kind: req.Kind, Resource: resource, OwnerRunID: req.OwnerRunID, OwnerJobID: req.OwnerJobID, FencingToken: state.NextFencingToken, AcquiredAt: now, ExpiresAt: now.Add(req.TTL)}
	state.Leases = append(state.Leases, lease)
	sortLeases(state.Leases)
	if err := s.writeLeasesLocked(state); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (s *Store) ReleaseLease(kind LeaseKind, resource, ownerRunID, ownerJobID string) (bool, error) {
	if err := validateHandle("owner_run_id", ownerRunID); err != nil {
		return false, err
	}
	if ownerJobID != "" {
		if err := validateHandle("owner_job_id", ownerJobID); err != nil {
			return false, err
		}
	}
	normalized, err := normalizeLeaseResource(kind, resource)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readLeasesLocked()
	if err != nil {
		return false, err
	}
	now := s.now()
	current := activeLeases(state.Leases, now)
	out := current[:0]
	released := false
	for _, lease := range current {
		if lease.Kind == kind && lease.Resource == normalized && lease.OwnerRunID == ownerRunID && lease.OwnerJobID == ownerJobID {
			released = true
			continue
		}
		out = append(out, lease)
	}
	state.Leases = out
	if released || len(current) != len(state.Leases) {
		if err := s.writeLeasesLocked(state); err != nil {
			return false, err
		}
	}
	return released, nil
}

func (s *Store) ListLeases() ([]Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readLeasesLocked()
	if err != nil {
		return nil, err
	}
	active := activeLeases(state.Leases, s.now())
	if len(active) != len(state.Leases) {
		state.Leases = active
		if err := s.writeLeasesLocked(state); err != nil {
			return nil, err
		}
	}
	sortLeases(active)
	return append([]Lease(nil), active...), nil
}

func normalizeLeaseResource(kind LeaseKind, resource string) (string, error) {
	resource = strings.TrimSpace(resource)
	if resource == "" {
		return "", errors.New("lease resource is required")
	}
	switch kind {
	case LeaseNamed:
		if len(resource) > 512 || strings.ContainsRune(resource, '\x00') {
			return "", errors.New("named lease resource is invalid")
		}
		return resource, nil
	case LeasePath:
		return canonicalLeasePath(resource)
	default:
		return "", errors.New("lease kind must be named or path")
	}
}

func canonicalLeasePath(resource string) (string, error) {
	abs, err := filepath.Abs(resource)
	if err != nil {
		return "", fmt.Errorf("resolve lease path: %w", err)
	}
	abs = filepath.Clean(abs)
	probe := abs
	suffix := make([]string, 0, 4)
	for {
		resolved, err := filepath.EvalSymlinks(probe)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return abs, nil
		}
		suffix = append(suffix, filepath.Base(probe))
		probe = parent
	}
}

type LeaseClaim struct {
	Kind         LeaseKind
	Resource     string
	OwnerRunID   string
	OwnerJobID   string
	FencingToken uint64
}

func (s *Store) RenewLease(claim LeaseClaim, ttl time.Duration) (Lease, error) {
	if ttl <= 0 || ttl > 24*time.Hour {
		return Lease{}, errors.New("lease TTL must be > 0 and <= 24h")
	}
	normalized, err := normalizeLeaseResource(claim.Kind, claim.Resource)
	if err != nil {
		return Lease{}, err
	}
	if err := validateHandle("owner_run_id", claim.OwnerRunID); err != nil {
		return Lease{}, err
	}
	if claim.OwnerJobID != "" {
		if err := validateHandle("owner_job_id", claim.OwnerJobID); err != nil {
			return Lease{}, err
		}
	}
	if claim.FencingToken == 0 {
		return Lease{}, errors.New("fencing token is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readLeasesLocked()
	if err != nil {
		return Lease{}, err
	}
	now := s.now()
	state.Leases = activeLeases(state.Leases, now)
	for i := range state.Leases {
		lease := &state.Leases[i]
		if lease.Kind == claim.Kind && lease.Resource == normalized && lease.OwnerRunID == claim.OwnerRunID && lease.OwnerJobID == claim.OwnerJobID && lease.FencingToken == claim.FencingToken {
			lease.ExpiresAt = now.Add(ttl)
			if err := s.writeLeasesLocked(state); err != nil {
				return Lease{}, err
			}
			return *lease, nil
		}
	}
	return Lease{}, fmt.Errorf("%w: lease claim is stale or no longer active", ErrLeaseConflict)
}

func (s *Store) ValidateLease(claim LeaseClaim) error {
	normalized, err := normalizeLeaseResource(claim.Kind, claim.Resource)
	if err != nil {
		return err
	}
	if claim.FencingToken == 0 {
		return errors.New("fencing token is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.readLeasesLocked()
	if err != nil {
		return err
	}
	now := s.now()
	active := activeLeases(state.Leases, now)
	for _, lease := range active {
		if lease.Kind == claim.Kind && lease.Resource == normalized && lease.OwnerRunID == claim.OwnerRunID && lease.OwnerJobID == claim.OwnerJobID && lease.FencingToken == claim.FencingToken {
			return nil
		}
	}
	return fmt.Errorf("%w: stale fencing token or inactive lease", ErrLeaseConflict)
}

func leasesConflict(a, b Lease) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind == LeaseNamed {
		return a.Resource == b.Resource
	}
	return pathContains(a.Resource, b.Resource) || pathContains(b.Resource, a.Resource)
}

func pathContains(parent, child string) bool {
	if parent == child {
		return true
	}
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func activeLeases(leases []Lease, now time.Time) []Lease {
	out := make([]Lease, 0, len(leases))
	for _, lease := range leases {
		if lease.ExpiresAt.After(now) {
			out = append(out, lease)
		}
	}
	return out
}

func sortLeases(leases []Lease) {
	sort.Slice(leases, func(i, j int) bool {
		if leases[i].Kind != leases[j].Kind {
			return leases[i].Kind < leases[j].Kind
		}
		return leases[i].Resource < leases[j].Resource
	})
}

func (s *Store) readLeasesLocked() (leaseFile, error) {
	path := filepath.Join(s.root, "leases.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return leaseFile{SchemaVersion: leaseSchemaVersion}, nil
	}
	if err != nil {
		return leaseFile{}, fmt.Errorf("read workflow leases: %w", err)
	}
	var state leaseFile
	if err := json.Unmarshal(data, &state); err != nil {
		return leaseFile{}, fmt.Errorf("decode workflow leases: %w", err)
	}
	if state.SchemaVersion != leaseSchemaVersion {
		return leaseFile{}, fmt.Errorf("unsupported workflow lease schema %d", state.SchemaVersion)
	}
	return state, nil
}

func (s *Store) writeLeasesLocked(state leaseFile) error {
	state.SchemaVersion = leaseSchemaVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode workflow leases: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(s.root, ".leases-*.tmp")
	if err != nil {
		return fmt.Errorf("create workflow lease temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(s.root, "leases.json")); err != nil {
		return fmt.Errorf("replace workflow leases: %w", err)
	}
	return nil
}
