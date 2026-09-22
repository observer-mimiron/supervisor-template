// Package application 定义应用层装配所需的最小依赖合同。
//
// 本层只持有用例边界，不绑定 Gin、Eino 或具体存储实现。
package application

import (
	"context"
	"errors"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/approval"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
	"github.com/observer-mimiron/supervisor-template/internal/domain/identity"
)

var (
	// ErrUnauthenticated 表示凭证不能解析为可信主体。
	ErrUnauthenticated = errors.New("身份凭证无效")
	// ErrAccessDenied 表示可信主体不拥有目标 run。
	ErrAccessDenied = errors.New("无权访问此执行")
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

// CheckpointReader 可选地暴露持久化读取错误；旧的内存实现仍可只实现 CheckpointStore。
type CheckpointReader interface {
	GetWithError(string) (agent.Checkpoint, bool, error)
}

// RepositoryReader 可选地暴露持久化读取错误，避免损坏或不兼容快照被当成不存在。
type RepositoryReader interface {
	LoadRequest(string) (conversation.ExecutionRequest, bool, error)
	LoadApproval(string) (approval.Request, bool, error)
	LoadPlan(string) (agent.ExecutionPlan, bool, error)
}

// EventStore 是应用层需要的有序事件合同。
type EventStore interface {
	Append(agent.RunEvent) (agent.RunEvent, error)
	Events(string) []agent.RunEvent
}

// Authenticator 将传输层提取的凭证解析为可信主体。
type Authenticator interface {
	Authenticate(context.Context, string) (identity.Subject, error)
}

// RunAuthorizer 判断主体是否可访问某个 run 的所有者。
// 它只负责用户资源授权，不处理模型提出的 Tool 权限。
type RunAuthorizer interface {
	Authorize(context.Context, identity.Subject, identity.Subject) error
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

// WorkerRequest 是已通过 Policy Gate 的单个 Worker 执行请求。
// 应用层只传递结构化上下文和幂等键，不把 HTTP、Eino 或具体模型暴露给 Runner。
type WorkerRequest struct {
	RunID          string
	WorkerID       string
	Intent         string
	ToolID         string
	Input          map[string]string
	IdempotencyKey string
	Deadline       time.Time
}

// WorkerResult 是 Worker 的受控输出；最终事件和公开文本仍由运行用例负责。
type WorkerResult struct {
	Content string
}

// WorkerRunner 是可替换的 Worker 执行合同。
// 普通函数、Eino ReAct 和 Graph 适配器都应实现该接口。
type WorkerRunner interface {
	Run(context.Context, WorkerRequest) (WorkerResult, error)
}

// SingleToolRunner 是当前 v1 的兼容 Runner：把一个有界 Worker 映射为一次已批准 Tool 调用。
// 它只作为默认适配器，后续执行策略可以替换而不改变 run.Service。
type SingleToolRunner struct {
	Tools ToolExecutor
}

// Run 执行 Worker 请求指定的唯一 Tool。
func (r SingleToolRunner) Run(ctx context.Context, request WorkerRequest) (WorkerResult, error) {
	if r.Tools == nil {
		return WorkerResult{}, errors.New("Worker Runner 未装配 Tool 执行器")
	}
	content, err := r.Tools.Execute(ctx, request.ToolID, request.Input, request.IdempotencyKey)
	if err != nil {
		return WorkerResult{}, err
	}
	return WorkerResult{Content: content}, nil
}

// Dependencies 是 Manager/应用用例的基础设施依赖集合。
type Dependencies struct {
	Repository Repository
	Checkpoint CheckpointStore
	EventBus   EventStore
	Supervisor DecisionProvider
	Policy     PolicyEvaluator
	RunAuth    RunAuthorizer
	Runner     WorkerRunner
	// Tools 保留给旧装配和合同测试；新运行链路优先使用 Runner。
	Tools ToolExecutor
}

// HealthService 暴露仅用于启动检查的依赖状态。
type HealthService struct {
	Dependencies Dependencies
}

// Healthy 判断应用依赖是否完整。
func (s HealthService) Healthy() bool {
	return s.Dependencies.Repository != nil && s.Dependencies.Checkpoint != nil &&
		s.Dependencies.EventBus != nil && s.Dependencies.Supervisor != nil &&
		s.Dependencies.Policy != nil && s.Dependencies.RunAuth != nil &&
		(s.Dependencies.Runner != nil || s.Dependencies.Tools != nil)
}
