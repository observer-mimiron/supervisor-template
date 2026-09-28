package tool

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestPoolBoundsConcurrentCallsAndRejectsExhaustionBeforeInvocation(t *testing.T) {
	pool, err := NewPool(1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	finish := make(chan struct{})
	var calls atomic.Int32
	if err := pool.Register("read", func(ctx context.Context, _ ToolInvocation) (string, error) {
		calls.Add(1)
		close(started)
		select {
		case <-finish:
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() {
		_, err := pool.Invoke(context.Background(), ToolInvocation{ToolID: "read"})
		firstDone <- err
	}()
	<-started
	if _, err := pool.Invoke(context.Background(), ToolInvocation{ToolID: "read"}); !errors.Is(err, ErrPoolCapacity) {
		t.Fatalf("exhausted call error = %v, want ErrPoolCapacity", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
	close(finish)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestPoolLeaseDeadlineHealthGenerationAndIdempotentRelease(t *testing.T) {
	pool, err := NewPool(1, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	if err := pool.Register("read", func(ctx context.Context, _ ToolInvocation) (string, error) {
		calls.Add(1)
		<-ctx.Done()
		return "", ctx.Err()
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := pool.Acquire(context.Background(), "read")
	if err != nil {
		t.Fatal(err)
	}
	if deadline, ok := lease.Deadline(); !ok || time.Until(deadline) <= 0 {
		t.Fatalf("lease deadline = %v, ok=%v", deadline, ok)
	}
	if err := pool.SetHealthy("read", false); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Invoke(context.Background(), ToolInvocation{ToolID: "read"}); !errors.Is(err, ErrToolUnhealthy) {
		t.Fatalf("unhealthy Tool error = %v, want ErrToolUnhealthy", err)
	}
	if _, err := lease.Invoke(ToolInvocation{ToolID: "read"}); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("stale lease error = %v, want ErrLeaseExpired", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("stale lease invoked handler %d times", got)
	}
	lease.Release()
	lease.Release()
	if err := pool.SetHealthy("read", true); err != nil {
		t.Fatal(err)
	}
	result, err := pool.Invoke(context.Background(), ToolInvocation{ToolID: "read"})
	if result != "" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed call = %q, %v", result, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
}

func TestPoolRejectsUnknownToolAndLeaseMismatchBeforeInvocation(t *testing.T) {
	pool, err := NewPool(1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	if err := pool.Register("registered", func(context.Context, ToolInvocation) (string, error) {
		calls.Add(1)
		return "ok", nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Invoke(context.Background(), ToolInvocation{ToolID: "unknown"}); !errors.Is(err, ErrToolUnregistered) {
		t.Fatalf("unknown tool error = %v", err)
	}
	lease, err := pool.Acquire(context.Background(), "registered")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Invoke(ToolInvocation{ToolID: "other"}); !errors.Is(err, ErrToolUnregistered) {
		t.Fatalf("mismatched tool error = %v", err)
	}
	lease.Release()
	if _, err := lease.Invoke(ToolInvocation{ToolID: "registered"}); !errors.Is(err, ErrLeaseReleased) {
		t.Fatalf("released lease error = %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("rejected calls invoked handler %d times", got)
	}
}

func TestRegistryRejectsUnknownAndInvalidInputsBeforePoolInvocation(t *testing.T) {
	registry, err := NewRegistry("", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), "unknown", map[string]string{"message": "x"}, ""); err == nil {
		t.Fatal("unknown tool was accepted")
	}
	if _, err := registry.Execute(context.Background(), "user_query", nil, ""); err == nil {
		t.Fatal("invalid input was accepted")
	}
	if got := registry.OutreachCount(); got != 0 {
		t.Fatalf("rejected calls caused %d side effects", got)
	}
}
