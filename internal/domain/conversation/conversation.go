// Package conversation 定义请求所属的会话边界。
//
// 本包只表达用户请求和会话标识，不决定路由、权限或 Tool 调用。
package conversation

import "time"

// ExecutionRequest 是一次用户发起的执行请求。
type ExecutionRequest struct {
	RunID          string
	ConversationID string
	Message        string
	RequestedAt    time.Time
	ResumeOf       string
}
