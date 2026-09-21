// Package eventbus 提供有序 RunEvent 的内存记录器。
//
// 事件总线只负责记录顺序和唯一终态，不负责推进业务状态或投影 HTTP 响应。
package eventbus

import (
	"errors"
	"sync"

	"github.com/observer-mimiron/suanming-agent/eino-supervisor-template/internal/domain/agent"
)

// EventType、RunEvent 和事件常量沿用领域合同，避免基础设施重新定义语义。
type EventType = agent.EventType
type RunEvent = agent.RunEvent

const (
	Started          = agent.Started
	Decision         = agent.Decision
	Plan             = agent.Plan
	Progress         = agent.Progress
	ToolCall         = agent.ToolCall
	ApprovalRequired = agent.ApprovalRequired
	Text             = agent.Text
	Completed        = agent.Completed
	Failed           = agent.Failed
	Canceled         = agent.Canceled
)

// MemoryBus 为每个 run 维护单调序列和事件列表。
type MemoryBus struct {
	mu     sync.RWMutex
	events map[string][]RunEvent
}

// NewMemoryBus 创建空的事件总线。
func NewMemoryBus() *MemoryBus { return &MemoryBus{events: make(map[string][]RunEvent)} }

// Append 追加事件，拒绝序列跳跃和重复终态。
func (b *MemoryBus) Append(event RunEvent) (RunEvent, error) {
	if event.RunID == "" || event.EventID == "" || event.Type == "" {
		return RunEvent{}, errors.New("事件缺少必要字段")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	items := b.events[event.RunID]
	expected := int64(len(items) + 1)
	if event.Sequence == 0 {
		event.Sequence = expected
	}
	if event.Sequence != expected {
		return RunEvent{}, errors.New("事件序列必须从 1 开始连续递增")
	}
	if event.Type == Completed || event.Type == Failed || event.Type == Canceled {
		for _, item := range items {
			if item.Type == Completed || item.Type == Failed || item.Type == Canceled {
				return RunEvent{}, errors.New("每个 run 只能有一个终态事件")
			}
		}
	}
	b.events[event.RunID] = append(items, event)
	return event, nil
}

// Events 返回某个 run 的事件副本，终态 run 可据此重放结果。
func (b *MemoryBus) Events(runID string) []RunEvent {
	b.mu.RLock()
	defer b.mu.RUnlock()
	items := b.events[runID]
	return append([]RunEvent(nil), items...)
}

// TerminalStatus 将终态事件映射回运行状态。
func TerminalStatus(eventType EventType) agent.RunStatus {
	switch eventType {
	case Completed:
		return agent.RunCompleted
	case Failed:
		return agent.RunFailed
	case Canceled:
		return agent.RunCanceled
	default:
		return agent.RunRunning
	}
}
