// Package eino 提供 Eino ADK 的运行适配。
//
// 本文件复用官方示例的 CheckPointStore 形态，负责 Eino 状态字节的内存保存；
// 它不替代领域 checkpoint，也不拥有业务运行状态。
package eino

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/compose"
)

// MemoryCheckpointStore 是 Eino 使用的并发安全内存 checkpoint。
type MemoryCheckpointStore struct {
	mu  sync.RWMutex
	mem map[string][]byte
}

// NewMemoryCheckpointStore 创建可注入 Eino Runner 的 checkpoint store。
func NewMemoryCheckpointStore() compose.CheckPointStore {
	return &MemoryCheckpointStore{mem: make(map[string][]byte)}
}

// Set 保存 checkpoint 字节的副本，避免调用方复用缓冲区造成状态漂移。
func (s *MemoryCheckpointStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mem[key] = append([]byte(nil), value...)
	return nil
}

// Get 返回 checkpoint 字节的副本。
func (s *MemoryCheckpointStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.mem[key]
	return append([]byte(nil), value...), ok, nil
}
