package tool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
)

var (
	ErrPoolCapacity     = errors.New("Tool Pool 容量已满")
	ErrToolUnhealthy    = errors.New("Tool Pool 中的 Tool 不健康")
	ErrLeaseExpired     = errors.New("Tool Pool 租约已过期")
	ErrLeaseReleased    = errors.New("Tool Pool 租约已释放")
	ErrToolUnregistered = errors.New("Tool Pool 中的 Tool 未注册")
)

// ToolInvocation 是 Registry 完成路由和输入校验后提交给 Pool 的调用。
type ToolInvocation struct {
	ToolID         string
	Input          map[string]string
	IdempotencyKey string
}

type toolInvoker func(context.Context, ToolInvocation) (string, error)

type poolEntry struct {
	invoke     toolInvoker
	healthy    bool
	generation uint64
}

// Pool bounds concurrent calls in this process. Registration is startup-only in normal use.
type Pool struct {
	mu          sync.RWMutex
	entries     map[string]poolEntry
	semaphore   chan struct{}
	callTimeout time.Duration
}

// NewPool creates a process-local bounded pool. A non-positive timeout means no added deadline.
func NewPool(maxConcurrent int, callTimeout time.Duration) (*Pool, error) {
	if maxConcurrent <= 0 {
		return nil, errors.New("Tool Pool 并发上限必须大于零")
	}
	return &Pool{entries: make(map[string]poolEntry), semaphore: make(chan struct{}, maxConcurrent), callTimeout: callTimeout}, nil
}

// Register adds one fixed tool handler. Duplicate or incomplete registrations are rejected.
func (p *Pool) Register(toolID string, invoke func(context.Context, ToolInvocation) (string, error)) error {
	if p == nil || toolID == "" || invoke == nil {
		return errors.New("Tool Pool 注册参数无效")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, exists := p.entries[toolID]; exists {
		return fmt.Errorf("Tool %q 已注册到 Pool", toolID)
	}
	p.entries[toolID] = poolEntry{invoke: invoke, healthy: true, generation: 1}
	return nil
}

// SetHealthy changes the Tool health state and invalidates leases from prior generations.
func (p *Pool) SetHealthy(toolID string, healthy bool) error {
	if p == nil {
		return ErrToolUnregistered
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.entries[toolID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrToolUnregistered, toolID)
	}
	if entry.healthy != healthy {
		entry.healthy = healthy
		entry.generation++
		p.entries[toolID] = entry
	}
	return nil
}

// Acquire obtains a lease without waiting when the bounded pool is exhausted.
func (p *Pool) Acquire(ctx context.Context, toolID string) (*ToolPoolLease, error) {
	if p == nil || ctx == nil {
		return nil, &application.PreCallError{Err: errors.New("Tool Pool 或调用上下文未装配")}
	}
	if err := ctx.Err(); err != nil {
		return nil, &application.PreCallError{Err: err}
	}
	p.mu.RLock()
	entry, ok := p.entries[toolID]
	p.mu.RUnlock()
	if !ok {
		return nil, &application.PreCallError{Err: fmt.Errorf("%w: %s", ErrToolUnregistered, toolID)}
	}
	if !entry.healthy {
		return nil, &application.PreCallError{Err: ErrToolUnhealthy}
	}
	select {
	case p.semaphore <- struct{}{}:
	default:
		return nil, &application.PreCallError{Err: ErrPoolCapacity}
	}
	if err := ctx.Err(); err != nil {
		<-p.semaphore
		return nil, &application.PreCallError{Err: err}
	}
	leaseCtx := ctx
	cancel := func() {}
	if p.callTimeout > 0 {
		leaseCtx, cancel = context.WithTimeout(ctx, p.callTimeout)
	}
	lease := &ToolPoolLease{pool: p, toolID: toolID, generation: entry.generation, ctx: leaseCtx, cancel: cancel}
	return lease, nil
}

// Invoke acquires and releases a lease around one validated invocation.
func (p *Pool) Invoke(ctx context.Context, invocation ToolInvocation) (string, error) {
	lease, err := p.Acquire(ctx, invocation.ToolID)
	if err != nil {
		return "", err
	}
	defer lease.Release()
	return lease.Invoke(invocation)
}

// ToolPoolLease owns one semaphore slot and a snapshot of the Tool health generation.
type ToolPoolLease struct {
	mu         sync.Mutex
	pool       *Pool
	toolID     string
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	released   bool
}

// Invoke rejects expired, stale, released, or mismatched leases before calling the handler.
func (l *ToolPoolLease) Invoke(invocation ToolInvocation) (string, error) {
	if l == nil {
		return "", &application.PreCallError{Err: ErrLeaseReleased}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return "", &application.PreCallError{Err: ErrLeaseReleased}
	}
	if err := l.ctx.Err(); err != nil {
		return "", &application.PreCallError{Err: fmt.Errorf("%w: %w", ErrLeaseExpired, err)}
	}
	if invocation.ToolID != l.toolID {
		return "", &application.PreCallError{Err: fmt.Errorf("%w: lease=%s invocation=%s", ErrToolUnregistered, l.toolID, invocation.ToolID)}
	}
	l.pool.mu.RLock()
	entry, ok := l.pool.entries[l.toolID]
	l.pool.mu.RUnlock()
	if !ok || entry.generation != l.generation {
		return "", &application.PreCallError{Err: ErrLeaseExpired}
	}
	if !entry.healthy {
		return "", &application.PreCallError{Err: ErrToolUnhealthy}
	}
	result, err := entry.invoke(l.ctx, invocation)
	return result, normalizeInvocationError(err)
}

// Release returns the semaphore slot exactly once and cancels the lease context.
func (l *ToolPoolLease) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	l.cancel()
	<-l.pool.semaphore
}

// Deadline exposes the effective per-call deadline for observability and tests.
func (l *ToolPoolLease) Deadline() (time.Time, bool) {
	if l == nil || l.ctx == nil {
		return time.Time{}, false
	}
	return l.ctx.Deadline()
}
