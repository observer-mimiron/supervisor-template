// Package application 定义应用层装配所需的最小依赖合同。
//
// 本层只持有用例边界，不绑定 Gin、Eino 或具体存储实现。
package application

import (
	"context"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/approval"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

// Repository 是应用层需要的请求和计划存储合同。
type Repository interface {
	SaveRequest(conversation.ExecutionRequest) error
	GetRequest(string) (conversation.ExecutionRequest, bool)
	SaveApproval(approval.Request) error
	GetApproval(string) (approval.Request, bool)
	SavePlan(agent.ExecutionPlan) error
	GetPlan(string) (agent.ExecutionPlan, bool)
}

// CheckpointStore 是应用层需要的恢复快照合同。
type CheckpointStore interface {
	Save(agent.Checkpoint) (agent.Checkpoint, error)
	Get(string) (agent.Checkpoint, bool)
}

// EventStore 是应用层需要的有序事件合同。
type EventStore interface {
	Append(agent.RunEvent) (agent.RunEvent, error)
	Events(string) []agent.RunEvent
}

// DecisionProvider 是 Supervisor 的应用层最小接口。
type DecisionProvider interface {
	Decide(context.Context, conversation.ExecutionRequest) (agent.SupervisorDecision, error)
}

// PolicyEvaluator 是确定性策略门控的应用层最小接口。
type PolicyEvaluator interface {
	Evaluate(agent.SupervisorDecision) (agent.ApprovedRoute, error)
}

// ToolExecutor 是 Worker 调用已批准 Tool 的应用层最小接口。
type ToolExecutor interface {
	Execute(context.Context, string, map[string]string, string) (string, error)
}

// Dependencies 是 Manager/应用用例的基础设施依赖集合。
type Dependencies struct {
	Repository Repository
	Checkpoint CheckpointStore
	EventBus   EventStore
	Supervisor DecisionProvider
	Policy     PolicyEvaluator
	Tools      ToolExecutor
}

// HealthService 暴露仅用于启动检查的依赖状态。
type HealthService struct {
	Dependencies Dependencies
}

// Healthy 判断应用依赖是否完整。
func (s HealthService) Healthy() bool {
	return s.Dependencies.Repository != nil && s.Dependencies.Checkpoint != nil &&
		s.Dependencies.EventBus != nil && s.Dependencies.Supervisor != nil &&
		s.Dependencies.Policy != nil && s.Dependencies.Tools != nil
}
