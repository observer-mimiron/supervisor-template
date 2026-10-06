// Package eino 提供 Eino ADK 的运行适配。
//
// 本文件实现 Eino 的 compose.CheckPointStore。
//
// 重要事实（由 checkpoint_reachability_test.go 实测固定）：ADK 只在取消、中断或
// 非空闲 Stop 时写入存储，而我们的取消是普通 context cancel、从不产生 Eino 中断，
// 因此这个 store 在当前链路上**从不被触碰**。它保留内存实现是因为 ADK 需要非 nil
// 的 store 才能构造 Runner。
//
// 若将来采用 Eino 的中断机制做人工审批，这里需要重新引入持久化实现——届时应由
// 那三条可达性测试变红来驱动，而不是提前建设。
package eino

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/compose"
)

// MemoryCheckpointStore 是 Eino 使用的并发安全内存 checkpoint。
//
// 它只在单进程内有效。在当前架构下这不是限制，而是事实：ADK 从不往 store 写东西
// （见文件头说明与可达性测试）。
type MemoryCheckpointStore struct {
	mu  sync.RWMutex
	mem map[string][]byte
}

// NewMemoryCheckpointStore 创建可注入 Eino Runner 的内存 checkpoint store。
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
