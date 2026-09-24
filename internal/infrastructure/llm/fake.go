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
	return agent.SupervisorDecision{
		DecisionID: request.RunID + ":decision",
		WorkerID:   route.WorkerID,
		Intent:     route.Intent,
		Arguments:  map[string]string{"tool_id": route.ToolID, "message": message},
		Risk:       route.Risk,
		Confidence: 1,
	}, nil
}
