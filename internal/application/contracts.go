// Package application 定义应用层装配所需的最小依赖合同。
//
// 本层只持有用例边界，不绑定 Gin、Eino 或具体存储实现。
package application

import (
	"context"
	"errors"
	"time"

	appmemory "github.com/observer-mimiron/supervisor-template/internal/application/memory"
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
	// ErrOutcomeUnknown 表示外部调用可能已生效，但结果提交未确认。
	ErrOutcomeUnknown = errors.New("外部执行结果未知")
)

// ExecutionPhase 标记错误发生在外部调用生命周期的哪个阶段。
type ExecutionPhase string

const (
	PhasePreCall    ExecutionPhase = "pre_call"
	PhaseInFlight   ExecutionPhase = "in_flight"
	PhaseCommit     ExecutionPhase = "commit"
	PhaseProjection ExecutionPhase = "projection"
)

// ErrorClass 是跨 Runner、Tool、HTTP 和 SSE 共享的稳定错误分类。
type ErrorClass = agent.ErrorClass

const (
	ErrorPreCallFailure = agent.ClassPreCallFailure
	ErrorTimeout        = agent.ClassTimeout
	ErrorCanceled       = agent.ClassCanceled
	ErrorInvalidOutput  = agent.ClassInvalidOutput
	ErrorPolicyDenied   = agent.ClassPolicyDenied
	ErrorBusiness       = agent.ClassBusiness
	ErrorUnavailable    = agent.ClassUnavailable
	ErrorUnknownOutcome = agent.ClassUnknownOutcome
	ErrorInternal       = agent.ClassInternal
)

// RetryDecision 是应用层对已分类错误的确定性处置。
type RetryDecision string

const (
	RetryNever RetryDecision = "never"
	Retry      RetryDecision = "retry"
	Reconcile  RetryDecision = "reconcile"
	Terminate  RetryDecision = "terminate"
)

// ErrorPolicy 将底层错误事实映射为统一分类和重试决策。
type ErrorPolicy interface {
	Classify(error, ExecutionPhase) ErrorClass
	Decide(ErrorClass, int, int) RetryDecision
}

// OutcomeUnknownError 由 Runner 用于明确声明调用已开始但结果未知。
type OutcomeUnknownError struct{ Err error }

func (e *OutcomeUnknownError) Error() string {
	if e == nil || e.Err == nil {
		return ErrOutcomeUnknown.Error()
	}
	return e.Err.Error()
}
func (e *OutcomeUnknownError) Unwrap() error { return ErrOutcomeUnknown }

// PreCallError 表示外部调用尚未开始、可以按预算重试的已知失败。
// Runner 不得在外部调用已经开始后使用此类型；未知结果必须使用 OutcomeUnknownError。
type PreCallError struct{ Err error }

func (e *PreCallError) Error() string {
	if e == nil || e.Err == nil {
		return "调用尚未开始"
	}
	return e.Err.Error()
}
func (e *PreCallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

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

// RunLease serializes one Run's execution and durable state mutations.
type RunLease struct {
	RunID      string
	OwnerToken string
	ExpiresAt  time.Time
}

// RunLeaseStore atomically claims, verifies and releases a Run lease.
type RunLeaseStore interface {
	Claim(context.Context, RunLease, time.Time) (bool, error)
	Owns(context.Context, RunLease, time.Time) (bool, error)
	Release(context.Context, RunLease) error
}

// ContextEventStore 是可选的 context-aware 事件扩展；旧事件存储仍可实现基础合同。
type ContextEventStore interface {
	AppendContext(context.Context, agent.RunEvent) (agent.RunEvent, error)
	EventsContext(context.Context, string) []agent.RunEvent
}

// ContextCheckpointStore 是可选的 context-aware checkpoint 扩展。
type ContextCheckpointStore interface {
	SaveContext(context.Context, agent.Checkpoint) (agent.Checkpoint, error)
	GetContext(context.Context, string) (agent.Checkpoint, bool)
}

// ContextCheckpointReader 是可选的 context-aware checkpoint 读取扩展。
type ContextCheckpointReader interface {
	GetWithContext(context.Context, string) (agent.Checkpoint, bool, error)
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

// RuntimeObserver is the narrow application-to-infrastructure observation port.
// Implementations must keep attributes low-cardinality and must not receive user payloads.
type RuntimeObserver interface {
	Observe(context.Context, RuntimeObservation)
}

type RuntimeObservation struct {
	RunID         string
	WorkerID      string
	ToolID        string
	Phase         string
	ErrorCode     string
	ErrorClass    string
	RetryDecision string
	Attempt       int
	Duration      time.Duration
}

// ToolExecutor 是 Worker 调用已批准 Tool 的应用层最小接口。
type ToolExecutor interface {
	Execute(context.Context, string, map[string]string, string) (string, error)
}

// ToolContractValidator 校验已注册 Tool 的结构化输入和输出。
// 实现位于 infrastructure；应用层只依赖这个窄合同，避免把注册表类型带入业务编排。
type ToolContractValidator interface {
	ValidateInput(string, map[string]string) error
	ValidateOutput(string, string) error
}

// ToolInvocation is a fully approved, schema-validated call prepared by the manager.
type ToolInvocation struct {
	RunID          string
	StepID         string
	ToolID         string
	Input          map[string]string
	IdempotencyKey string
	Deadline       time.Time
}

// ToolPoolLease is the resource-only lease acquired for one invocation.
type ToolPoolLease interface {
	Invoke(ToolInvocation) (string, error)
	Release()
}

// ToolInvoker owns bounded Tool resources, not policy, approval, or run state.
type ToolInvoker interface {
	Acquire(context.Context, ToolInvocation) (ToolPoolLease, error)
}

// RunnerSession contains checkpoint identity and bounded budgets for one run.
type RunnerSession struct {
	RunID       string
	Checkpoint  string
	Deadline    time.Time
	ToolBudget  int
	ModelBudget int
}

// WorkerRequest 是已通过 Policy Gate 的单个 Worker 执行请求。
// 应用层只传递结构化上下文和幂等键，不把 HTTP、Eino 或具体模型暴露给 Runner。
type WorkerRequest struct {
	RunID          string
	Session        RunnerSession
	Step           agent.PlanStep
	ToolBudget     int
	ModelBudget    int
	ResumeToken    string
	WorkerID       string
	Intent         string
	ToolID         string
	Input          map[string]string
	IdempotencyKey string
	Deadline       time.Time
	Memories       []conversation.MemoryEntry
}

// WorkerResult 是 Worker 的受控输出；最终事件和公开文本仍由运行用例负责。
type WorkerResult struct {
	Content    string
	Checkpoint string
}

// WorkerRunner 是可替换的 Worker 执行合同。
// 普通函数、Eino ReAct 和 Graph 适配器都应实现该接口。
type WorkerRunner interface {
	Run(context.Context, WorkerRequest) (WorkerResult, error)
}

// ExecutionBudget 是运行期唯一的步骤、调用、重试、成本和时间上限。
// v1 将一次 Tool attempt 计为一个成本单位，便于保持单 Tool Runner 的最小合同。
type ExecutionBudget struct {
	MaxPlanSteps int
	MaxToolCalls int
	MaxRetries   int
	CostBudget   int
	Timeout      time.Duration
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
	Repository    Repository
	Checkpoint    CheckpointStore
	EventBus      EventStore
	Leases        RunLeaseStore
	Supervisor    DecisionProvider
	Policy        PolicyEvaluator
	RunAuth       RunAuthorizer
	Runner        WorkerRunner
	Budget        ExecutionBudget
	ToolValidator ToolContractValidator
	MemoryStore   appmemory.Store
	MemoryRead    appmemory.Retriever
	Observer      RuntimeObserver
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
		s.Dependencies.EventBus != nil && s.Dependencies.Leases != nil && s.Dependencies.Supervisor != nil &&
		s.Dependencies.Policy != nil && s.Dependencies.RunAuth != nil &&
		(s.Dependencies.Runner != nil || s.Dependencies.Tools != nil)
}
