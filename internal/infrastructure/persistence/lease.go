package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
)

// MemoryRunLeaseStore provides process-local leases for tests and memory mode.
type MemoryRunLeaseStore struct {
	mu    sync.Mutex
	items map[string]application.RunLease
}

func NewMemoryRunLeaseStore() *MemoryRunLeaseStore {
	return &MemoryRunLeaseStore{items: make(map[string]application.RunLease)}
}

func (s *MemoryRunLeaseStore) Claim(ctx context.Context, lease application.RunLease, now time.Time) (bool, error) {
	if err := validateRunLease(ctx, lease, now); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.items[lease.RunID]; ok && current.ExpiresAt.After(now) {
		return false, nil
	}
	s.items[lease.RunID] = lease
	return true, nil
}

func (s *MemoryRunLeaseStore) Owns(ctx context.Context, lease application.RunLease, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.items[lease.RunID]
	return ok && current.OwnerToken == lease.OwnerToken && current.ExpiresAt.After(now), nil
}

func (s *MemoryRunLeaseStore) Release(ctx context.Context, lease application.RunLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.items[lease.RunID]; ok && current.OwnerToken == lease.OwnerToken {
		delete(s.items, lease.RunID)
	}
	return nil
}

// FileRunLeaseStore coordinates cooperating processes on a local filesystem.
type FileRunLeaseStore struct{ dir string }

func NewFileRunLeaseStore(dir string) (*FileRunLeaseStore, error) {
	if dir == "" {
		return nil, errors.New("run lease 目录不能为空")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建 run lease 目录失败: %w", err)
	}
	return &FileRunLeaseStore{dir: dir}, nil
}

func (s *FileRunLeaseStore) Claim(ctx context.Context, lease application.RunLease, now time.Time) (bool, error) {
	if err := validateRunLease(ctx, lease, now); err != nil {
		return false, err
	}
	claimed := false
	err := s.withLock(ctx, lease.RunID, func() error {
		current, found, err := s.read(lease.RunID)
		if err != nil {
			return err
		}
		if found && current.ExpiresAt.After(now) {
			return nil
		}
		if err := s.write(lease); err != nil {
			return err
		}
		claimed = true
		return nil
	})
	return claimed, err
}

func (s *FileRunLeaseStore) Owns(ctx context.Context, lease application.RunLease, now time.Time) (bool, error) {
	var owns bool
	err := s.withLock(ctx, lease.RunID, func() error {
		current, found, err := s.read(lease.RunID)
		if err != nil {
			return err
		}
		owns = found && current.OwnerToken == lease.OwnerToken && current.ExpiresAt.After(now)
		return nil
	})
	return owns, err
}

func (s *FileRunLeaseStore) Release(ctx context.Context, lease application.RunLease) error {
	return s.withLock(ctx, lease.RunID, func() error {
		current, found, err := s.read(lease.RunID)
		if err != nil || !found || current.OwnerToken != lease.OwnerToken {
			return err
		}
		if err := os.Remove(s.statePath(lease.RunID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除 run lease 失败: %w", err)
		}
		return nil
	})
}

func (s *FileRunLeaseStore) withLock(ctx context.Context, runID string, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.lockPath(runID), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("打开 run lease 锁失败: %w", err)
	}
	defer lock.Close()
	for {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		} else if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			return fmt.Errorf("获取 run lease 锁失败: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func (s *FileRunLeaseStore) read(runID string) (application.RunLease, bool, error) {
	data, err := os.ReadFile(s.statePath(runID))
	if errors.Is(err, os.ErrNotExist) {
		return application.RunLease{}, false, nil
	}
	if err != nil {
		return application.RunLease{}, false, fmt.Errorf("读取 run lease 失败: %w", err)
	}
	var lease application.RunLease
	if err := json.Unmarshal(data, &lease); err != nil {
		return application.RunLease{}, false, fmt.Errorf("解析 run lease 失败: %w", err)
	}
	return lease, true, nil
}

func (s *FileRunLeaseStore) write(lease application.RunLease) error {
	data, err := json.Marshal(lease)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".lease-*.tmp")
	if err != nil {
		return fmt.Errorf("创建 run lease 临时文件失败: %w", err)
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
	if err := os.Rename(name, s.statePath(lease.RunID)); err != nil {
		return fmt.Errorf("替换 run lease 失败: %w", err)
	}
	return nil
}

func (s *FileRunLeaseStore) lockPath(runID string) string {
	return filepath.Join(s.dir, leaseFilename(runID)+".lock")
}

func (s *FileRunLeaseStore) statePath(runID string) string {
	return filepath.Join(s.dir, leaseFilename(runID)+".json")
}

func leaseFilename(runID string) string {
	digest := sha256.Sum256([]byte(runID))
	return hex.EncodeToString(digest[:])
}

func validateRunLease(ctx context.Context, lease application.RunLease, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if lease.RunID == "" || lease.OwnerToken == "" || !lease.ExpiresAt.After(now) {
		return errors.New("run lease 参数非法")
	}
	return nil
}
