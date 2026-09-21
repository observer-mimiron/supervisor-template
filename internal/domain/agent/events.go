// Package agent 定义运行事件合同，供事件记录和传输投影共同使用。
//
// 事件属于领域运行记录，不依赖 SSE、Gin、Eino 或具体事件总线。
package agent

import "time"

// EventType 是运行事件类型。
type EventType string

const (
	Started          EventType = "started"
	Decision         EventType = "decision"
	Plan             EventType = "plan"
	Progress         EventType = "progress"
	ToolCall         EventType = "tool_call"
	ApprovalRequired EventType = "approval_required"
	Text             EventType = "text"
	Completed        EventType = "completed"
	Failed           EventType = "failed"
	Canceled         EventType = "canceled"
)

// RunEvent 是可重放的内部运行事件；公开投影不得包含敏感原文。
type RunEvent struct {
	EventID        string
	RunID          string
	Sequence       int64
	Type           EventType
	OccurredAt     time.Time
	Data           map[string]string
	RedactionClass string
}
