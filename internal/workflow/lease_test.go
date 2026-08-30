package workflow

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func leaseTestStore(t *testing.T) (*Store, Run) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Create(CreateRequest{Title: "lease owner"})
	if err != nil {
		t.Fatal(err)
	}
	return store, run
}

func TestPathLeasesConflictOnAncestorAndAllowSiblings(t *testing.T) {
	store, run := leaseTestStore(t)
	other, err := store.Create(CreateRequest{Title: "other"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	left := filepath.Join(root, "left")
	if _, err := store.AcquireLease(LeaseRequest{Kind: LeasePath, Resource: left, OwnerRunID: run.ID, OwnerJobID: "a", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLease(LeaseRequest{Kind: LeasePath, Resource: filepath.Join(left, "file.go"), OwnerRunID: other.ID, OwnerJobID: "b", TTL: time.Hour}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("nested conflict error = %v", err)
	}
	if _, err := store.AcquireLease(LeaseRequest{Kind: LeasePath, Resource: filepath.Join(root, "right"), OwnerRunID: other.ID, OwnerJobID: "b", TTL: time.Hour}); err != nil {
		t.Fatalf("non-overlapping path rejected: %v", err)
	}
}

func TestNamedLeaseIsIdempotentForSameOwner(t *testing.T) {
	store, run := leaseTestStore(t)
	first, err := store.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "release", OwnerRunID: run.ID, OwnerJobID: "a", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "release", OwnerRunID: run.ID, OwnerJobID: "a", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("idempotent lease changed: %+v != %+v", first, second)
	}
}

func TestExpiredLeaseIsRecoveredAndPersisted(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	one, _ := store.Create(CreateRequest{Title: "one"})
	two, _ := store.Create(CreateRequest{Title: "two"})
	if _, err := store.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "x", OwnerRunID: one.ID, TTL: time.Minute}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	lease, err := store.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "x", OwnerRunID: two.ID, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if lease.OwnerRunID != two.ID {
		t.Fatalf("expired lease not recovered: %+v", lease)
	}
	reloaded, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = func() time.Time { return now }
	leases, err := reloaded.ListLeases()
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 1 || leases[0].OwnerRunID != two.ID {
		t.Fatalf("persisted leases = %+v", leases)
	}
}

func TestReleaseLeaseRequiresOwnerIdentity(t *testing.T) {
	store, run := leaseTestStore(t)
	if _, err := store.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "x", OwnerRunID: run.ID, OwnerJobID: "a", TTL: time.Hour}); err != nil {
		t.Fatal(err)
	}
	released, err := store.ReleaseLease(LeaseNamed, "x", run.ID, "other")
	if err != nil {
		t.Fatal(err)
	}
	if released {
		t.Fatal("different job released lease")
	}
	released, err = store.ReleaseLease(LeaseNamed, "x", run.ID, "a")
	if err != nil || !released {
		t.Fatalf("owner release = %v, %v", released, err)
	}
}
