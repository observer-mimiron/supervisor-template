package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
)

func TestMemoryRunLeaseStoreClaimExpiryAndOwnerRelease(t *testing.T) {
	store := NewMemoryRunLeaseStore()
	now := time.Unix(100, 0)
	first := application.RunLease{RunID: "run-1", OwnerToken: "owner-1", ExpiresAt: now.Add(time.Minute)}
	second := application.RunLease{RunID: first.RunID, OwnerToken: "owner-2", ExpiresAt: now.Add(2 * time.Minute)}
	claimed, err := store.Claim(context.Background(), first, now)
	if err != nil || !claimed {
		t.Fatalf("first claim = %v, %v", claimed, err)
	}
	claimed, err = store.Claim(context.Background(), second, now)
	if err != nil || claimed {
		t.Fatalf("live competing claim = %v, %v", claimed, err)
	}
	if err := store.Release(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	owned, err := store.Owns(context.Background(), first, now)
	if err != nil || !owned {
		t.Fatalf("non-owner release changed lease: owns=%v err=%v", owned, err)
	}
	claimed, err = store.Claim(context.Background(), second, first.ExpiresAt)
	if err != nil || !claimed {
		t.Fatalf("expired takeover = %v, %v", claimed, err)
	}
	owned, err = store.Owns(context.Background(), first, first.ExpiresAt)
	if err != nil || owned {
		t.Fatalf("old owner still owns expired lease: owns=%v err=%v", owned, err)
	}
	if err := store.Release(context.Background(), second); err != nil {
		t.Fatal(err)
	}
}

func TestFileRunLeaseStoreCoordinatesInstances(t *testing.T) {
	dir := t.TempDir()
	first, err := NewFileRunLeaseStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileRunLeaseStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(200, 0)
	lease1 := application.RunLease{RunID: "run-file", OwnerToken: "owner-1", ExpiresAt: now.Add(time.Minute)}
	lease2 := application.RunLease{RunID: lease1.RunID, OwnerToken: "owner-2", ExpiresAt: now.Add(2 * time.Minute)}
	claimed, err := first.Claim(context.Background(), lease1, now)
	if err != nil || !claimed {
		t.Fatalf("first file claim = %v, %v", claimed, err)
	}
	claimed, err = second.Claim(context.Background(), lease2, now)
	if err != nil || claimed {
		t.Fatalf("second file claim = %v, %v", claimed, err)
	}
	claimed, err = second.Claim(context.Background(), lease2, lease1.ExpiresAt)
	if err != nil || !claimed {
		t.Fatalf("file expiry takeover = %v, %v", claimed, err)
	}
	owned, err := first.Owns(context.Background(), lease1, lease1.ExpiresAt)
	if err != nil || owned {
		t.Fatalf("old file owner still owns lease: %v, %v", owned, err)
	}
	if err := first.Release(context.Background(), lease1); err != nil {
		t.Fatal(err)
	}
	owned, err = second.Owns(context.Background(), lease2, lease1.ExpiresAt)
	if err != nil || !owned {
		t.Fatalf("non-owner release removed current lease: %v, %v", owned, err)
	}
	if err := second.Release(context.Background(), lease2); err != nil {
		t.Fatal(err)
	}
}

func TestRunLeaseStoreRejectsInvalidAndCanceledClaims(t *testing.T) {
	store := NewMemoryRunLeaseStore()
	now := time.Now()
	if _, err := store.Claim(context.Background(), application.RunLease{RunID: "run", OwnerToken: "owner", ExpiresAt: now}, now); err == nil {
		t.Fatal("expected expired lease rejection")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.Claim(ctx, application.RunLease{RunID: "run", OwnerToken: "owner", ExpiresAt: now.Add(time.Minute)}, now)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled claim error = %v", err)
	}
}
