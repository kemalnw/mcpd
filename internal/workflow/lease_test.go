package workflow

import (
	"errors"
	"os"
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

func TestLeaseFencingTokenAdvancesAcrossExpiryAndRejectsStaleOwner(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	one, _ := store.Create(CreateRequest{Title: "one"})
	two, _ := store.Create(CreateRequest{Title: "two"})
	first, err := store.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "release", OwnerRunID: one.ID, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if first.FencingToken == 0 {
		t.Fatal("missing first fencing token")
	}
	now = now.Add(2 * time.Minute)
	second, err := store.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "release", OwnerRunID: two.ID, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if second.FencingToken <= first.FencingToken {
		t.Fatalf("fence did not advance: first=%d second=%d", first.FencingToken, second.FencingToken)
	}
	if err := store.ValidateLease(LeaseClaim{Kind: first.Kind, Resource: first.Resource, OwnerRunID: first.OwnerRunID, FencingToken: first.FencingToken}); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("stale first owner validated: %v", err)
	}
	if err := store.ValidateLease(LeaseClaim{Kind: second.Kind, Resource: second.Resource, OwnerRunID: second.OwnerRunID, FencingToken: second.FencingToken}); err != nil {
		t.Fatal(err)
	}
}

func TestRenewLeaseRequiresCurrentOwnerAndFence(t *testing.T) {
	store, run := leaseTestStore(t)
	lease, err := store.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "build", OwnerRunID: run.ID, OwnerJobID: "job", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := store.RenewLease(LeaseClaim{Kind: lease.Kind, Resource: lease.Resource, OwnerRunID: lease.OwnerRunID, OwnerJobID: lease.OwnerJobID, FencingToken: lease.FencingToken}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.FencingToken != lease.FencingToken || !renewed.ExpiresAt.After(lease.ExpiresAt) {
		t.Fatalf("bad renewal: old=%+v new=%+v", lease, renewed)
	}
	if _, err := store.RenewLease(LeaseClaim{Kind: lease.Kind, Resource: lease.Resource, OwnerRunID: lease.OwnerRunID, OwnerJobID: lease.OwnerJobID, FencingToken: lease.FencingToken + 1}, time.Hour); !errors.Is(err, ErrLeaseConflict) {
		t.Fatalf("wrong fence renewed: %v", err)
	}
}

func TestLeaseFencingTokenPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	one, _ := store.Create(CreateRequest{Title: "one"})
	first, err := store.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "x", OwnerRunID: one.ID, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseLease(first.Kind, first.Resource, first.OwnerRunID, first.OwnerJobID); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := reopened.Create(CreateRequest{Title: "two"})
	second, err := reopened.AcquireLease(LeaseRequest{Kind: LeaseNamed, Resource: "x", OwnerRunID: two.ID, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if second.FencingToken <= first.FencingToken {
		t.Fatalf("restart reset fence: first=%d second=%d", first.FencingToken, second.FencingToken)
	}
}

func TestCanonicalLeasePathResolvesSymlinkedExistingParentForFuturePath(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	throughAlias, err := normalizeLeaseResource(LeasePath, filepath.Join(alias, "future", "file.go"))
	if err != nil {
		t.Fatal(err)
	}
	throughReal, err := normalizeLeaseResource(LeasePath, filepath.Join(real, "future", "file.go"))
	if err != nil {
		t.Fatal(err)
	}
	if throughAlias != throughReal {
		t.Fatalf("symlink parent alias bypass: alias=%q real=%q", throughAlias, throughReal)
	}
}
