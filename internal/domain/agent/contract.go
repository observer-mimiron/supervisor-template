// Package agent 定义执行计划、路由和运行状态的领域合同。
//
// 本包拥有状态不变量和终态规则，不依赖 HTTP、Eino、数据库、MCP 或具体模型。
package agent

import (
	"errors"
	"fmt"
	"time"
)

// Risk 表示能力对外部状态的影响等级。
type Risk string

const (
	RiskReadOnly   Risk = "read_only"
	RiskSideEffect Risk = "side_effect"
)

// RunStatus 表示一次执行的生命周期状态。
type RunStatus string

const (
	RunPending               RunStatus = "pending"
	RunRunning               RunStatus = "running"
	RunWaitingApproval       RunStatus = "waiting_approval"
	RunWaitingReconciliation RunStatus = "waiting_reconciliation"
	RunCompleted             RunStatus = "completed"
	RunFailed                RunStatus = "failed"
	RunCanceled              RunStatus = "canceled"
)

// StepStatus 表示单个计划步骤的状态。
type StepStatus string

const (
	StepPending               StepStatus = "pending"
	StepRunning               StepStatus = "running"
	StepSucceeded             StepStatus = "succeeded"
	StepFailed                StepStatus = "failed"
	StepWaitingApproval       StepStatus = "waiting_approval"
	StepWaitingReconciliation StepStatus = "waiting_reconciliation"
	StepCanceled              StepStatus = "canceled"
)

// ErrorCode 是可公开分类的稳定错误码。
type ErrorCode string

// ErrorClass is the stable cross-adapter classification of a failed operation.
type ErrorClass string

const (
	ClassPreCallFailure ErrorClass = "pre_call_failure"
	ClassTimeout        ErrorClass = "timeout"
	ClassCanceled       ErrorClass = "canceled"
	ClassInvalidOutput  ErrorClass = "invalid_output"
	ClassPolicyDenied   ErrorClass = "policy_denied"
	ClassBusiness       ErrorClass = "business_failure"
	ClassUnavailable    ErrorClass = "unavailable"
	ClassUnknownOutcome ErrorClass = "unknown_outcome"
	ClassInternal       ErrorClass = "internal"
)

const (
	ErrorUnknownCapability ErrorCode = "UNKNOWN_CAPABILITY"
	ErrorPolicyDenied      ErrorCode = "POLICY_DENIED"
	ErrorInvalidState      ErrorCode = "INVALID_STATE"
	ErrorBudgetExceeded    ErrorCode = "BUDGET_EXCEEDED"
	ErrorToolTimeout       ErrorCode = "TOOL_TIMEOUT"
	ErrorCanceled          ErrorCode = "CANCELED"
	ErrorInvalidOutput     ErrorCode = "INVALID_OUTPUT"
	ErrorInternal          ErrorCode = "INTERNAL_ERROR"
	ErrorApprovalRequired  ErrorCode = "APPROVAL_REQUIRED"
	ErrorRunNotResumable   ErrorCode = "RUN_NOT_RESUMABLE"
	ErrorUnauthenticated   ErrorCode = "UNAUTHENTICATED"
	ErrorAccessDenied      ErrorCode = "ACCESS_DENIED"
	ErrorOutcomeUnknown    ErrorCode = "RUN_OUTCOME_UNKNOWN"
)

// SupervisorDecision 是模型或 Supervisor 提出的候选路由，不代表授权结果。
type SupervisorDecision struct {
	DecisionID string
	WorkerID   string
	Intent     string
	Arguments  map[string]string
	Risk       Risk
	Confidence float64
}

// ApprovedRoute 是策略门控后的确定性路由结果。
type ApprovedRoute struct {
	DecisionID       string
	WorkerID         string
	AllowedTools     []string
	ApprovalRequired bool
	PolicyVersion    string
}

// PlanStep 是执行计划中的一个有序步骤。
type PlanStep struct {
	StepID         string
	WorkerID       string
	Intent         string
	ToolID         string
	Input          map[string]string
	Status         StepStatus
	Attempts       int
	IdempotencyKey string
	ResultDigest   string
	ResultContent  string
	AttemptStatus  string
}

// ExecutionPlan 是 Manager 所有的有界执行计划。
type ExecutionPlan struct {
	PlanID       string
	RunID        string
	Steps        []PlanStep
	MaxSteps     int
	Deadline     time.Time
	Status       RunStatus
	TerminalCode ErrorCode
}

// NewExecutionPlan 创建一个状态为 pending 的有界计划。
func NewExecutionPlan(planID, runID string, steps []PlanStep, maxSteps int, deadline time.Time) (ExecutionPlan, error) {
	if planID == "" || runID == "" || len(steps) == 0 || maxSteps <= 0 || len(steps) > maxSteps {
		return ExecutionPlan{}, errors.New("执行计划参数不合法")
	}
	for i := range steps {
		if steps[i].StepID == "" || steps[i].ToolID == "" || steps[i].Status != StepPending {
			return ExecutionPlan{}, errors.New("执行计划包含非法步骤")
		}
	}
	steps = append([]PlanStep(nil), steps...)
	for i := range steps {
		if steps[i].Input != nil {
			input := make(map[string]string, len(steps[i].Input))
			for key, value := range steps[i].Input {
				input[key] = value
			}
			steps[i].Input = input
		}
	}
	return ExecutionPlan{PlanID: planID, RunID: runID, Steps: steps, MaxSteps: maxSteps, Deadline: deadline, Status: RunPending}, nil
}

// TransitionStep 执行单个步骤的合法状态转换并维护计划状态。
func (p *ExecutionPlan) TransitionStep(stepID string, next StepStatus) error {
	for index := range p.Steps {
		if p.Steps[index].StepID != stepID {
			continue
		}
		current := p.Steps[index].Status
		if !validStepTransition(current, next) {
			return fmt.Errorf("步骤 %s 不允许从 %s 转为 %s: %w", stepID, current, next, errors.New(string(ErrorInvalidState)))
		}
		p.Steps[index].Status = next
		if next == StepRunning && p.Status == RunPending {
			p.Status = RunRunning
		}
		if next == StepWaitingApproval {
			p.Status = RunWaitingApproval
		}
		if next == StepWaitingReconciliation {
			p.Status = RunWaitingReconciliation
		}
		if next == StepFailed {
			p.Status = RunFailed
		}
		if next == StepCanceled {
			p.Status = RunCanceled
		}
		if allStepsSucceeded(p.Steps) {
			p.Status = RunCompleted
		}
		return nil
	}
	return fmt.Errorf("未知步骤 %q: %w", stepID, errors.New(string(ErrorUnknownCapability)))
}

// MarkTerminal 固定计划终态，拒绝覆盖已经存在的终态。
func (p *ExecutionPlan) MarkTerminal(status RunStatus, code ErrorCode) error {
	if !isTerminal(status) {
		return errors.New("只能写入终态")
	}
	if isTerminal(p.Status) && p.Status != status {
		return errors.New("终态不可覆盖")
	}
	p.Status = status
	p.TerminalCode = code
	return nil
}

// Checkpoint 是恢复执行所需的最小快照。
type Checkpoint struct {
	RunID            string
	PlanID           string
	NextStepID       string
	CompletedStepIDs []string
	Status           RunStatus
	Version          int64
	SavedAt          time.Time
}

// IsTerminal 判断运行状态是否不可再推进。
func IsTerminal(status RunStatus) bool { return isTerminal(status) }

func validStepTransition(current, next StepStatus) bool {
	switch current {
	case StepPending:
		return next == StepRunning || next == StepWaitingApproval || next == StepCanceled
	case StepRunning:
		return next == StepSucceeded || next == StepFailed || next == StepCanceled || next == StepWaitingReconciliation
	case StepWaitingApproval:
		return next == StepRunning || next == StepFailed || next == StepCanceled
	case StepWaitingReconciliation:
		return false
	default:
		return false
	}
}

func allStepsSucceeded(steps []PlanStep) bool {
	for _, step := range steps {
		if step.Status != StepSucceeded {
			return false
		}
	}
	return true
}

func isTerminal(status RunStatus) bool {
	return status == RunCompleted || status == RunFailed || status == RunCanceled
}
