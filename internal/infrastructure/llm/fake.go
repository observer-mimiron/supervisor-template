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
	build  func(conversation.ExecutionRequest, string, string, string, agent.Risk) agent.SupervisorDecision
}

// NewFakeSupervisor 创建确定性 fake Supervisor。
func NewFakeSupervisor(routes ...FakeRoute) *FakeSupervisor {
	if len(routes) == 0 {
		routes = []FakeRoute{{ToolID: "not_registered", Risk: agent.RiskReadOnly}}
	}
	return NewFakeSupervisorWithBuilder(routes, nil)
}

// NewFakeSupervisorWithBuilder creates a deterministic Supervisor with an
// explicit payload/decision builder supplied by composition.
func NewFakeSupervisorWithBuilder(routes []FakeRoute, build func(conversation.ExecutionRequest, string, string, string, agent.Risk) agent.SupervisorDecision) *FakeSupervisor {
	cloned := make([]FakeRoute, len(routes))
	for index, route := range routes {
		cloned[index] = route
		cloned[index].Matches = append([]string(nil), route.Matches...)
	}
	return &FakeSupervisor{routes: cloned, build: build}
}

// Decide 将只读问题和模拟触达问题映射到已声明 Tool。
func (s *FakeSupervisor) Decide(_ context.Context, request conversation.ExecutionRequest) (agent.SupervisorDecision, error) {
	message := strings.TrimSpace(request.Message)
	if message == "" {
		return agent.SupervisorDecision{}, context.Canceled
	}
	var route FakeRoute
	found := false
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
			found = true
			break
		}
	}
	if !found && len(s.routes) == 1 && len(s.routes[0].Matches) == 0 {
		route = s.routes[0]
	}
	if s.build != nil {
		return s.build(request, route.WorkerID, route.Intent, route.ToolID, route.Risk), nil
	}
	decision := agent.SupervisorDecision{
		DecisionID: request.RunID + ":decision",
		WorkerID:   route.WorkerID,
		Intent:     route.Intent,
		Arguments:  map[string]string{"tool_id": route.ToolID, "message": message},
		Risk:       route.Risk,
		Confidence: 1,
	}
	return decision, nil
}
