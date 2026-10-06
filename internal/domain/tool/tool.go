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
	// Summary 是注册方提供的、给人看的动作说明，用于审批请求与审计记录。
	// 它不参与任何授权判断，因此可以为空（此时审批记录的摘要为空）。
	Summary        string
	RequiredInputs []string
	MaxInputBytes  int
	MaxOutputBytes int
}
