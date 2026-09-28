// Package llm 提供模型边界的 fake 实现。
//
// M1 默认使用确定性 Supervisor，保持本地合同测试不依赖凭证；真实模型适配放在后续阶段。
package llm

import (
	"context"
	"strings"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
)

// FakeRoute 是 fake Supervisor 使用的配置路由快照。
type FakeRoute struct {
	WorkerID string
	Intent   string
	Matches  []string
	ToolID   string
	Risk     agent.Risk
}

// FakeSupervisor 根据用户消息生成稳定候选路由，不生成权限、审批结果或终态。
type FakeSupervisor struct {
	routes []FakeRoute
}

const fixedAudienceQuery = "{\"as_of\":\"2026-09-25\"}"
const fixedAudienceProjection = "{\"count\":4,\"customer_ids\":[\"cust-001\",\"cust-002\",\"cust-006\",\"cust-008\"],\"spend_365d_total\":6200}"
const fixedOrderQuery = "{\"user_id\":1}"
const fixedOrderInsert = "{\"user_id\":1,\"product_id\":1,\"quantity\":1,\"total_amount\":\"19.90\"}"

// NewFakeSupervisor 创建确定性 fake Supervisor。
func NewFakeSupervisor(routes ...FakeRoute) *FakeSupervisor {
	if len(routes) == 0 {
		for _, route := range examplebusiness.Routes() {
			routes = append(routes, FakeRoute{
				WorkerID: route.WorkerID,
				Intent:   route.Intent,
				Matches:  append([]string(nil), route.Matches...),
				ToolID:   route.ToolID,
				Risk:     route.Risk,
			})
		}
	}
	cloned := make([]FakeRoute, len(routes))
	for index, route := range routes {
		cloned[index] = route
		cloned[index].Matches = append([]string(nil), route.Matches...)
	}
	return &FakeSupervisor{routes: cloned}
}

// Decide 将只读问题和模拟触达问题映射到已声明 Tool。
func (s *FakeSupervisor) Decide(_ context.Context, request conversation.ExecutionRequest) (agent.SupervisorDecision, error) {
	message := strings.TrimSpace(request.Message)
	if message == "" {
		return agent.SupervisorDecision{}, context.Canceled
	}
	route := FakeRoute{WorkerID: examplebusiness.WorkerID, Intent: examplebusiness.WorkerID, ToolID: "not_registered", Risk: agent.RiskReadOnly}
	for _, candidate := range s.routes {
		matched := false
		for _, match := range candidate.Matches {
			if strings.Contains(message, match) {
				route = candidate
				matched = true
				break
			}
		}
		if matched {
			break
		}
	}
	if strings.Contains(message, "未知能力") {
		route.ToolID = "not_registered"
	}
	toolMessage := message
	switch route.ToolID {
	case examplebusiness.ReadOnlyToolID:
		toolMessage = fixedAudienceQuery
	case examplebusiness.SummaryToolID, examplebusiness.SideEffectToolID:
		toolMessage = fixedAudienceProjection
	case examplebusiness.MySQLQueryToolID:
		toolMessage = fixedOrderQuery
	case examplebusiness.MySQLInsertToolID:
		toolMessage = fixedOrderInsert
	}
	decision := agent.SupervisorDecision{
		DecisionID: request.RunID + ":decision",
		WorkerID:   route.WorkerID,
		Intent:     route.Intent,
		Arguments:  map[string]string{"tool_id": route.ToolID, "message": toolMessage},
		Risk:       route.Risk,
		Confidence: 1,
	}
	// The example multi-worker fixture is intentionally explicit and bounded:
	// it is a serial two-step candidate, never an open-ended model loop.
	if isSerialExample(message) {
		first, firstOK := examplebusiness.RouteFor(examplebusiness.ReadOnlyToolID)
		second, secondOK := examplebusiness.RouteFor(examplebusiness.SummaryToolID)
		if firstOK && secondOK {
			decision.Steps = []agent.CandidateStep{
				{WorkerID: first.WorkerID, Intent: first.Intent, Arguments: map[string]string{"tool_id": first.ToolID, "message": fixedAudienceQuery}, Risk: first.Risk},
				{WorkerID: second.WorkerID, Intent: second.Intent, Arguments: map[string]string{"tool_id": second.ToolID, "message": fixedAudienceProjection}, Risk: second.Risk},
			}
			decision.WorkerID, decision.Intent = first.WorkerID, first.Intent
			decision.Arguments, decision.Risk = decision.Steps[0].Arguments, decision.Steps[0].Risk
		}
	}
	return decision, nil
}

func isSerialExample(message string) bool {
	return (strings.Contains(message, "串行") || strings.Contains(message, "多步骤") || strings.Contains(message, "先") && strings.Contains(message, "再")) && !strings.Contains(message, "未知能力")
}
