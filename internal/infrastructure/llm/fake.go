// Package llm 提供模型边界的 fake 实现。
//
// M1 默认使用确定性 Supervisor，保持本地合同测试不依赖凭证；真实模型适配放在后续阶段。
package llm

import (
	"context"
	"strings"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

// FakeSupervisor 根据用户消息生成稳定候选路由，不生成权限、审批结果或终态。
type FakeSupervisor struct{}

// NewFakeSupervisor 创建确定性 fake Supervisor。
func NewFakeSupervisor() *FakeSupervisor { return &FakeSupervisor{} }

// Decide 将只读问题和模拟触达问题映射到已声明 Tool。
func (s *FakeSupervisor) Decide(_ context.Context, request conversation.ExecutionRequest) (agent.SupervisorDecision, error) {
	message := strings.TrimSpace(request.Message)
	if message == "" {
		return agent.SupervisorDecision{}, context.Canceled
	}
	toolID := "user_query"
	risk := agent.RiskReadOnly
	if strings.Contains(message, "触达") || strings.Contains(message, "发送") || strings.Contains(message, "模拟写入") {
		toolID = "simulated_outreach"
		risk = agent.RiskSideEffect
	}
	if strings.Contains(message, "未知能力") {
		toolID = "not_registered"
	}
	return agent.SupervisorDecision{
		DecisionID: request.RunID + ":decision",
		WorkerID:   "user_analysis",
		Intent:     "user_analysis",
		Arguments:  map[string]string{"tool_id": toolID, "message": message},
		Risk:       risk,
		Confidence: 1,
	}, nil
}
