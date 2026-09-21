// Package checkpoint 提供可替换的内存恢复快照实现。
//
// checkpoint 只服务恢复，不创建新的业务状态所有者；每次保存都递增版本。
package checkpoint

import (
	"errors"
	"sync"

	"github.com/observer-mimiron/suanming-agent/eino-supervisor-template/internal/domain/agent"
)

// MemoryStore 保存每个 run 的最新 checkpoint。
type MemoryStore struct {
	mu    sync.RWMutex
	items map[string]agent.Checkpoint
}

// NewMemoryStore 创建空的 checkpoint 存储。
func NewMemoryStore() *MemoryStore { return &MemoryStore{items: make(map[string]agent.Checkpoint)} }

// Save 保存快照并保证版本单调递增。
func (s *MemoryStore) Save(snapshot agent.Checkpoint) (agent.Checkpoint, error) {
	if snapshot.RunID == "" || snapshot.PlanID == "" {
		return agent.Checkpoint{}, errors.New("checkpoint 缺少 run_id 或 plan_id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, ok := s.items[snapshot.RunID]
	if ok {
		if snapshot.Version != previous.Version+1 {
			return agent.Checkpoint{}, errors.New("checkpoint 版本必须连续递增")
		}
	} else if snapshot.Version != 1 {
		return agent.Checkpoint{}, errors.New("首个 checkpoint 版本必须为 1")
	}
	s.items[snapshot.RunID] = snapshot
	return snapshot, nil
}

// Get 读取某个 run 的最新快照。
func (s *MemoryStore) Get(runID string) (agent.Checkpoint, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot, ok := s.items[runID]
	return snapshot, ok
}
