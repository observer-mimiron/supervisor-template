// Package tool 定义工具能力、安全风险和重试合同。
//
// 本包只描述 Tool，不执行外部调用，也不依赖 MCP、HTTP 或具体 SDK。
package tool

import "time"

// Contract 描述一个已注册 Tool 的安全边界。
type Contract struct {
	ToolID              string
	Implementation      string
	Risk                string
	Timeout             time.Duration
	RetryLimit          int
	RequiresApproval    bool
	IdempotencyRequired bool
	RequiredInputs      []string
	MaxInputBytes       int
	MaxOutputBytes      int
}
