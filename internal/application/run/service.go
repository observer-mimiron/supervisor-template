// Package run 负责一次执行的生命周期和恢复入口。
//
// 本包是运行状态 owner：负责 Supervisor -> Policy -> Plan -> Tool -> Event 的有界编排；
// 不绑定 Gin、Eino 或具体 Tool 实现，传输层只能读取本包生成的事件。
package run

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/approval"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
	"github.com/observer-mimiron/supervisor-template/internal/domain/identity"
)

// Error 是可映射为公开稳定分类的运行错误。
type Error struct {
	Code    agent.ErrorCode
	Message string
	cause   error
}

// Error 返回不包含内部堆栈和凭证的用户可理解消息。
func (e *Error) Error() string { return e.Message }

// Unwrap 保留内部持久化错误供进程内诊断，同时公开消息仍保持稳定。
func (e *Error) Unwrap() error { return e.cause }

// Service 是单进程运行状态和 ExecutionPlan 的唯一 owner。
type Service struct {
	mu        sync.Mutex
	deps      application.Dependencies
	ctx       context.Context
	approvals map[string]*approval.Request
	controlMu sync.Mutex
	active    map[string]*runControl
	sequence  atomic.Uint64
	now       func() time.Time
	lease     application.RunLease
}

type runControl struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewService 创建运行服务；依赖必须由 composition 统一装配。
func NewService(deps application.Dependencies) *Service {
	return &Service{deps: deps, approvals: make(map[string]*approval.Request), active: make(map[string]*runControl), now: time.Now}
}

// claimLease acquires the cross-process Run lease when one is configured.
// Legacy unit tests may omit the optional port; those tests retain the original
// in-process mutex semantics, while composition always supplies a durable store.
func (s *Service) claimLease(ctx context.Context, runID string) (application.RunLease, error) {
	if s.deps.Leases == nil {
		return application.RunLease{}, nil
	}
	if err := ctx.Err(); err != nil {
		return application.RunLease{}, err
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return application.RunLease{}, fmt.Errorf("生成执行租约 owner token 失败: %w", err)
	}
	ownerToken := hex.EncodeToString(token)
	ttl := s.budget().Timeout
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	if ttl < 5*time.Second {
		ttl = 5 * time.Second
	}
	// The lease is deliberately longer than one bounded run. Renewal for
	// unbounded work is deferred; bounded execution avoids a background goroutine.
	lease := application.RunLease{RunID: runID, OwnerToken: ownerToken, ExpiresAt: s.now().Add(2 * ttl)}
	claimed, err := s.deps.Leases.Claim(ctx, lease, s.now())
	if err != nil {
		return application.RunLease{}, err
	}
	if !claimed {
		return application.RunLease{}, &Error{Code: agent.ErrorRunBusy, Message: "执行正在由其他 owner 处理"}
	}
	s.lease = lease
	return lease, nil
}

func (s *Service) ensureLease(ctx context.Context, lease application.RunLease) error {
	if s.deps.Leases == nil || lease.RunID == "" {
		return nil
	}
	owned, err := s.deps.Leases.Owns(ctx, lease, s.now())
	if err != nil {
		return err
	}
	if !owned {
		return &Error{Code: agent.ErrorLeaseLost, Message: "执行租约已失效"}
	}
	return nil
}

func (s *Service) releaseLease(ctx context.Context, lease application.RunLease) error {
	if s.deps.Leases == nil || lease.RunID == "" {
		return nil
	}
	return s.deps.Leases.Release(context.WithoutCancel(ctx), lease)
}

// savePlanLocked is the single application boundary for durable Plan writes.
// It stamps update times without moving state ownership into persistence.
func (s *Service) savePlanLocked(plan agent.ExecutionPlan) error {
	if err := s.ensureLease(s.operationContext(), s.lease); err != nil {
		return err
	}
	now := s.now()
	plan.UpdatedAt = now
	for index := range plan.Steps {
		plan.Steps[index].UpdatedAt = now
	}
	return s.deps.Repository.SavePlan(plan)
}

func (s *Service) ensureCurrentLease() error {
	return s.ensureLease(s.operationContext(), s.lease)
}

// Start 接收一次用户请求，返回稳定 run_id；重复提交同一 run_id 只重放原事件。
func (s *Service) Start(ctx context.Context, request conversation.ExecutionRequest) (runID string, retErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
	defer func() { s.ctx = nil }()
	if request.RunID == "" {
		request.RunID = s.newRunID()
	}
	s.observe(ctx, application.RuntimeObservation{RunID: request.RunID, Phase: "run.start"})
	if !request.Subject.Valid() {
		return request.RunID, &Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"}
	}
	if request.ConversationID == "" || strings.TrimSpace(request.Message) == "" {
		return request.RunID, &Error{Code: "INVALID_REQUEST", Message: "会话和消息不能为空"}
	}
	lease, err := s.claimLease(ctx, request.RunID)
	if err != nil {
		return request.RunID, err
	}
	defer func() {
		if releaseErr := s.releaseLease(ctx, lease); releaseErr != nil && retErr == nil {
			retErr = &Error{Code: agent.ErrorLeaseLost, Message: "执行租约释放失败", cause: releaseErr}
		}
		s.lease = application.RunLease{}
	}()
	if existing := s.deps.EventBus.Events(request.RunID); len(existing) > 0 {
		stored, ok, err := s.loadRequest(request.RunID)
		if err != nil {
			return request.RunID, err
		}
		if !ok {
			return request.RunID, &Error{Code: agent.ErrorAccessDenied, Message: "无权访问此执行"}
		}
		if err := s.authorize(ctx, request.Subject, stored.Subject); err != nil {
			return request.RunID, err
		}
		if stored.ConversationID != request.ConversationID {
			return request.RunID, &Error{Code: agent.ErrorRunNotResumable, Message: "run_id 不属于当前会话"}
		}
		return request.RunID, nil
	}
	if request.RequestedAt.IsZero() {
		request.RequestedAt = s.now()
	}
	budget := s.budget()
	if err := s.ensureLease(ctx, lease); err != nil {
		return request.RunID, err
	}
	if err := s.deps.Repository.SaveRequest(request); err != nil {
		return request.RunID, err
	}
	if err := ctx.Err(); err != nil {
		// Record an already-canceled request without allowing its canceled
		// context to block the durable audit events.
		previousContext := s.ctx
		s.ctx = context.WithoutCancel(ctx)
		defer func() { s.ctx = previousContext }()
		if emitErr := s.emitLocked(request.RunID, agent.Started, map[string]string{"conversation_id": request.ConversationID}); emitErr != nil {
			return request.RunID, emitErr
		}
		if emitErr := s.emitLocked(request.RunID, agent.Canceled, map[string]string{"code": string(agent.ErrorCanceled), "message": "执行已取消"}); emitErr != nil {
			return request.RunID, emitErr
		}
		return request.RunID, &Error{Code: agent.ErrorCanceled, Message: "执行已取消"}
	}
	if err := s.emitLocked(request.RunID, agent.Started, map[string]string{"conversation_id": request.ConversationID}); err != nil {
		return request.RunID, err
	}
	decision, err := s.deps.Supervisor.Decide(ctx, request)
	s.observe(ctx, application.RuntimeObservation{RunID: request.RunID, Phase: "run.decision"})
	if err != nil {
		_ = s.failLocked(request.RunID, "INTERNAL_ERROR", "无法生成路由决策")
		return request.RunID, err
	}
	if err := s.emitLocked(request.RunID, agent.Decision, map[string]string{
		"worker_id":  decision.WorkerID,
		"tool_id":    decision.Arguments["tool_id"],
		"step_count": fmt.Sprintf("%d", decisionStepCount(decision)),
	}); err != nil {
		return request.RunID, err
	}
	candidates := decisionCandidates(decision)
	if budget := s.budget(); len(candidates) == 0 || len(candidates) > budget.MaxPlanSteps {
		message := fmt.Sprintf("候选计划超过配置上限 %d 步", budget.MaxPlanSteps)
		if len(candidates) == 0 {
			message = "候选计划为空"
		}
		_ = s.failLocked(request.RunID, string(agent.ErrorBudgetExceeded), message)
		return request.RunID, &Error{Code: agent.ErrorBudgetExceeded, Message: message}
	}
	steps := make([]agent.PlanStep, 0, len(candidates))
	for index, candidate := range candidates {
		if candidate.WorkerID == "" || candidate.Intent == "" || candidate.Arguments == nil || candidate.Arguments["tool_id"] == "" || candidate.Arguments["message"] == "" {
			_ = s.failLocked(request.RunID, string(agent.ErrorPolicyDenied), "候选步骤缺少必要字段")
			return request.RunID, &Error{Code: agent.ErrorPolicyDenied, Message: "候选步骤缺少必要字段"}
		}
		route, routeErr := s.deps.Policy.Evaluate(agent.SupervisorDecision{
			DecisionID: request.RunID + ":decision",
			WorkerID:   candidate.WorkerID,
			Intent:     candidate.Intent,
			Arguments:  cloneMap(candidate.Arguments),
			Risk:       candidate.Risk,
			Confidence: decision.Confidence,
		})
		if routeErr != nil {
			code := classifyPolicyError(routeErr)
			_ = s.failLocked(request.RunID, string(code), publicPolicyMessage(code))
			return request.RunID, &Error{Code: code, Message: publicPolicyMessage(code)}
		}
		stepID := fmt.Sprintf("%s:step-%d", request.RunID, index+1)
		steps = append(steps, agent.PlanStep{StepID: stepID, WorkerID: candidate.WorkerID, Intent: candidate.Intent, ToolID: route.AllowedTools[0], Input: toolInput(candidate.Arguments), Status: agent.StepPending, ApprovalRequired: route.ApprovalRequired, ActionSummary: route.ActionSummary, IdempotencyKey: stepID})
	}
	plan, err := agent.NewExecutionPlan(request.RunID+":plan", request.RunID, steps, budget.MaxPlanSteps, s.now().Add(budget.Timeout))
	if err != nil {
		_ = s.failLocked(request.RunID, "INTERNAL_ERROR", "无法创建执行计划")
		return request.RunID, err
	}
	if err := s.savePlanLocked(plan); err != nil {
		return request.RunID, err
	}
	planData := map[string]string{"plan_id": plan.PlanID, "step_count": fmt.Sprintf("%d", len(steps))}
	for index, step := range steps {
		planData[fmt.Sprintf("step_%d_worker_id", index+1)] = step.WorkerID
		planData[fmt.Sprintf("step_%d_tool_id", index+1)] = step.ToolID
	}
	if err := s.emitLocked(request.RunID, agent.Plan, planData); err != nil {
		return request.RunID, err
	}
	// 审批不再在这里按计划整体触发：暂停点后移到"该步骤的输入已确定之后"。
	// 计划构建时后续步骤的输入还是占位符（会被前一步结果改写），此时审批等于
	// 批准一个当时还不存在的动作。执行推进到该步骤时由 executeLocked 负责。
	if err := s.executeLocked(ctx, plan); err != nil {
		return request.RunID, err
	}
	return request.RunID, nil
}

// Approve 写入认证主体的审批决定；批准只解除门控，实际 Tool 调用由 Resume 推进。
//
// stepID 是这次批准针对的步骤。为空时取该 Run 当前等待审批的步骤（HTTP 的 step_id
// 缺省语义）。显式传入时必须正好是当前等待审批的那一步：不允许把批准写给别的步骤，
// 也不允许给一个没有待审批记录的动作"预授权"。
func (s *Service) Approve(ctx context.Context, subject identity.Subject, runID, stepID, decision string) (retErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
	defer func() { s.ctx = nil }()
	if err := s.authorizeRun(ctx, subject, runID); err != nil {
		return err
	}
	lease, err := s.claimLease(ctx, runID)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := s.releaseLease(ctx, lease); releaseErr != nil && retErr == nil {
			retErr = &Error{Code: agent.ErrorLeaseLost, Message: "执行租约释放失败", cause: releaseErr}
		}
		s.lease = application.RunLease{}
	}()
	// 审批按步骤绑定：先由计划解析出当前等待审批的步骤，再读该步骤自己的记录。
	// 未指定步骤参数时（HTTP step_id 缺省）走的也是这条路径。
	plan, found, err := s.loadPlan(runID)
	if err != nil {
		return err
	}
	if !found {
		return &Error{Code: "RUN_NOT_RESUMABLE", Message: "当前执行不等待审批"}
	}
	waitingStepID := pendingApprovalStepID(plan)
	if stepID != "" && stepID != waitingStepID {
		// 该步骤此刻不等待审批。若它已经有一条终态记录，说明这是对既有决定的重复提交，
		// 保持"重复审批是幂等 no-op"的既有语义；否则是调用方指错了动作，必须拒绝。
		if existing, ok, approvalErr := s.approvalLocked(runID, stepID); approvalErr != nil {
			return approvalErr
		} else if ok && existing.Status != approval.Pending {
			return nil
		}
		return &Error{Code: "RUN_NOT_RESUMABLE", Message: "该步骤当前不等待审批"}
	}
	stepID = waitingStepID
	request, ok, err := s.approvalLocked(runID, stepID)
	if err != nil {
		return err
	}
	if !ok {
		return &Error{Code: "RUN_NOT_RESUMABLE", Message: "当前执行不等待审批"}
	}
	status := approval.Rejected
	if decision == "approve" {
		status = approval.Approved
	}
	if decision != "approve" && decision != "reject" {
		return &Error{Code: "INVALID_REQUEST", Message: "审批决定只能是 approve 或 reject"}
	}
	if request.Status != approval.Pending {
		return nil
	}
	if request.ExpiredBy(s.now(), s.deps.ApprovalTimeout) {
		if err := s.expireApprovalLocked(&plan, request); err != nil {
			return err
		}
		return &Error{Code: agent.ErrorApprovalRequired, Message: "审批已超时"}
	}
	if err := request.Decide(status, subject.SubjectID, s.now()); err != nil {
		return &Error{Code: "INVALID_REQUEST", Message: err.Error()}
	}
	if err := s.ensureCurrentLease(); err != nil {
		return err
	}
	if err := s.deps.Repository.SaveApproval(*request); err != nil {
		return err
	}
	if request.Status == approval.Rejected {
		s.observe(ctx, application.RuntimeObservation{RunID: runID, Phase: "approval.reject"})
		if err := s.terminateLocked(&plan, agent.RunFailed, agent.ErrorPolicyDenied, "审批已拒绝"); err != nil {
			if _, ok := err.(*Error); !ok {
				return err
			}
		}
	}
	return nil
}

// Cancel 将主体拥有的未终态执行标记为 canceled，并持久化唯一取消事件。
func (s *Service) Cancel(ctx context.Context, subject identity.Subject, runID string) (retErr error) {
	if err := s.authorizeRun(ctx, subject, runID); err != nil {
		return err
	}
	if control := s.activeRun(runID); control != nil {
		control.cancel()
		select {
		case <-control.done:
		case <-time.After(250 * time.Millisecond):
			return &Error{Code: agent.ErrorOutcomeUnknown, Message: "执行尚未观察到取消信号"}
		case <-ctx.Done():
			return &Error{Code: agent.ErrorOutcomeUnknown, Message: "取消请求未完成"}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
	defer func() { s.ctx = nil }()
	lease, err := s.claimLease(ctx, runID)
	if err != nil {
		return err
	}
	defer func() {
		if releaseErr := s.releaseLease(ctx, lease); releaseErr != nil && retErr == nil {
			retErr = &Error{Code: agent.ErrorLeaseLost, Message: "执行租约释放失败", cause: releaseErr}
		}
		s.lease = application.RunLease{}
	}()
	plan, ok, err := s.loadPlan(runID)
	if err != nil {
		return err
	}
	if !ok {
		return &Error{Code: agent.ErrorRunNotResumable, Message: "执行不存在或不可取消"}
	}
	if agent.IsTerminal(plan.Status) {
		return nil
	}
	if request, ok, err := s.approvalLocked(runID, pendingApprovalStepID(plan)); err != nil {
		return err
	} else if ok && request.Status == approval.Pending {
		_ = request.Decide(approval.Expired, "", s.now())
		if err := s.ensureCurrentLease(); err != nil {
			return err
		}
		if err := s.deps.Repository.SaveApproval(*request); err != nil {
			return err
		}
	}
	if err := s.terminateLocked(&plan, agent.RunCanceled, agent.ErrorCanceled, "执行已取消"); err != nil {
		if _, ok := err.(*Error); !ok {
			return err
		}
	}
	s.observe(ctx, application.RuntimeObservation{RunID: runID, Phase: "run.cancel", ErrorCode: string(agent.ErrorCanceled)})
	return nil
}

// Resume 从 checkpoint 恢复主体拥有的执行；终态 run 只返回原事件，不再次调用 Tool。
func (s *Service) Resume(ctx context.Context, subject identity.Subject, runID string) (events []agent.RunEvent, retErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
	defer func() { s.ctx = nil }()
	if err := s.authorizeRun(ctx, subject, runID); err != nil {
		return nil, err
	}
	lease, err := s.claimLease(ctx, runID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if releaseErr := s.releaseLease(ctx, lease); releaseErr != nil && retErr == nil {
			retErr = &Error{Code: agent.ErrorLeaseLost, Message: "执行租约释放失败", cause: releaseErr}
		}
		s.lease = application.RunLease{}
	}()
	plan, ok, err := s.loadPlan(runID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &Error{Code: "RUN_NOT_RESUMABLE", Message: "执行不存在或不可恢复"}
	}
	if snapshot, found, err := s.loadCheckpoint(runID); err != nil {
		return nil, err
	} else if found && snapshot.PlanID != plan.PlanID {
		return nil, &Error{Code: "RUN_NOT_RESUMABLE", Message: "checkpoint 与执行计划不匹配"}
	}
	if err := s.reconcileExpiredInFlightLocked(&plan); err != nil {
		return s.deps.EventBus.Events(runID), err
	}
	if agent.IsTerminal(plan.Status) {
		if err := s.repairStepProjectionsLocked(plan); err != nil {
			return s.deps.EventBus.Events(runID), err
		}
		if err := s.repairTerminalEventLocked(plan); err != nil {
			return s.deps.EventBus.Events(runID), err
		}
		return s.deps.EventBus.Events(runID), nil
	}
	s.observe(ctx, application.RuntimeObservation{RunID: runID, Phase: "run.resume"})
	if err := s.repairStepProjectionsLocked(plan); err != nil {
		return s.deps.EventBus.Events(runID), err
	}
	if plan.Status == agent.RunWaitingReconciliation {
		return s.deps.EventBus.Events(runID), &Error{Code: agent.ErrorOutcomeUnknown, Message: "外部执行结果待人工核对"}
	}
	if s.activeRun(runID) != nil {
		return s.deps.EventBus.Events(runID), &Error{Code: agent.ErrorInvalidState, Message: "执行正在运行"}
	}
	approvalRequest, ok, err := s.approvalLocked(runID, pendingApprovalStepID(plan))
	if err != nil {
		return nil, err
	}
	if ok && approvalRequest.ExpiredBy(s.now(), s.deps.ApprovalTimeout) {
		if err := s.expireApprovalLocked(&plan, approvalRequest); err != nil {
			return s.deps.EventBus.Events(runID), err
		}
		return s.deps.EventBus.Events(runID), &Error{Code: agent.ErrorApprovalRequired, Message: "审批已超时"}
	}
	if ok && approvalRequest.Status == approval.Pending {
		return nil, &Error{Code: "APPROVAL_REQUIRED", Message: "执行仍等待审批"}
	}
	if err := s.executeLocked(ctx, plan); err != nil {
		return s.deps.EventBus.Events(runID), err
	}
	// 推进时可能停在一个**新的**动作上请求审批（首个副作用步骤，或参数已变而重新
	// 请求）。Resume 的既有合同是"仍等待审批"以 APPROVAL_REQUIRED 报出，调用方据此
	// 去取新的审批请求；这里保持该合同，不把暂停伪装成成功推进。
	if waiting, found, err := s.loadPlan(runID); err != nil {
		return s.deps.EventBus.Events(runID), err
	} else if found && waiting.Status == agent.RunWaitingApproval {
		return s.deps.EventBus.Events(runID), &Error{Code: "APPROVAL_REQUIRED", Message: "执行仍等待审批"}
	}
	return s.deps.EventBus.Events(runID), nil
}

// Events 返回 run 的事件快照，供 SSE 只读投影。
func (s *Service) Events(runID string) []agent.RunEvent { return s.deps.EventBus.Events(runID) }

func (s *Service) operationContext() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}

// approvalLocked 先读进程缓存，再从 Repository 恢复指定步骤的审批快照，
// 支持重启后的 resume。缓存与存储同键，避免"读到了别的步骤的批准"。
//
// stepID 为空表示"没有可绑定的步骤"，直接按不存在处理：空指针不能退化成
// "取该 run 的任意一条批准"，否则计划级审批的漏洞会从合同层重新长回来。
func (s *Service) approvalLocked(runID, stepID string) (*approval.Request, bool, error) {
	if stepID == "" {
		return nil, false, nil
	}
	if request := s.approvals[approvalCacheKey(runID, stepID)]; request != nil {
		return request, true, nil
	}
	request, ok, err := s.loadApproval(runID, stepID)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	s.approvals[approvalCacheKey(runID, stepID)] = &request
	return &request, true, nil
}

// pendingApprovalStepID 返回该计划当前等待审批的步骤。
//
// 逐个批准（spec §3.2）保证任一时刻至多一个步骤处于 waiting_approval，因此这个
// 解析是无歧义的；没有这样的步骤时返回空串，调用方按"没有待审批记录"处理。
func pendingApprovalStepID(plan agent.ExecutionPlan) string {
	for _, step := range plan.Steps {
		if step.Status == agent.StepWaitingApproval {
			return step.StepID
		}
	}
	return ""
}

// approvalCacheKey 用 (run_id, step_id) 组成进程内审批缓存的键。
func approvalCacheKey(runID, stepID string) string { return runID + "\x00" + stepID }

// ensureStepApprovalLocked 在推进一个副作用步骤前，确保它**自己**已获批准。
//
// 这是动作级审批的唯一门控：审批绑定 (step_id, tool_id, 输入摘要)，因此
//
//   - 只读步骤直接放行（FR-003），不产生审批请求；
//   - 其他步骤的批准、同一 Tool 别的步骤的批准、以及参数已变的旧批准，都不算通过
//     （FR-002 的"不得复用其他步骤、其他动作或其他参数的批准"）；
//   - 同一指纹的重试沿用原批准，不重复打扰操作者（spec §3.1）。
//
// 返回 paused=true 表示本次调用已经建立了审批请求并把该步骤停在 waiting_approval，
// 调用方必须结束本次推进。
func (s *Service) ensureStepApprovalLocked(ctx context.Context, plan *agent.ExecutionPlan, step *agent.PlanStep) (bool, error) {
	if !step.ApprovalRequired {
		return false, nil
	}
	digest := approval.ComputeActionDigest(step.StepID, step.ToolID, step.Input)
	existing, found, err := s.approvalLocked(plan.RunID, step.StepID)
	if err != nil {
		return false, err
	}
	if found {
		if existing.ExpiredBy(s.now(), s.deps.ApprovalTimeout) {
			return false, s.expireApprovalLocked(plan, existing)
		}
		if existing.BindsAction(digest) {
			return false, nil
		}
		if existing.Status == approval.Rejected {
			// 已拒绝的动作不得因为再次推进而复活；终止而不是重新询问。
			s.observe(ctx, application.RuntimeObservation{RunID: plan.RunID, Phase: "approval.reject", WorkerID: step.WorkerID, ToolID: step.ToolID})
			return false, s.terminateLocked(plan, agent.RunFailed, agent.ErrorPolicyDenied, "审批已拒绝")
		}
		if existing.Status == approval.Expired {
			return false, s.expireApprovalLocked(plan, existing)
		}
	}
	return true, s.requestStepApprovalLocked(ctx, plan, step, digest, existing, found)
}

// requestStepApprovalLocked 为该步骤建立（或按新参数重建）审批请求并暂停。
//
// 动作摘要来自计划步骤，而计划步骤的 ActionSummary 由 ApprovedRoute 从 Tool 注册
// 描述投影而来，因此应用层不需要、也不允许知道任何业务文案（FR-005）。
func (s *Service) requestStepApprovalLocked(ctx context.Context, plan *agent.ExecutionPlan, step *agent.PlanStep, digest string, existing *approval.Request, found bool) error {
	approvalID := step.StepID + ":approval"
	record := approval.Request{
		ApprovalID:    approvalID,
		RunID:         plan.RunID,
		StepID:        step.StepID,
		ToolID:        step.ToolID,
		ActionDigest:  digest,
		ActionSummary: step.ActionSummary,
		Risk:          string(agent.RiskSideEffect),
		Status:        approval.Pending,
		RequestedAt:   s.now(),
	}
	if found && existing != nil {
		// 同一 (run, step) 上的旧记录不保留终态：动作指纹已变，说明这是另一个动作，
		// 需要一次新的、针对新参数的批准。
		record.ApprovalID = existing.ApprovalID
	}
	if step.Status != agent.StepWaitingApproval {
		if err := plan.TransitionStep(step.StepID, agent.StepWaitingApproval); err != nil {
			return err
		}
	}
	if err := s.savePlanLocked(*plan); err != nil {
		return err
	}
	if err := s.ensureCurrentLease(); err != nil {
		return err
	}
	if err := s.deps.Repository.SaveApproval(record); err != nil {
		return err
	}
	s.approvals[approvalCacheKey(plan.RunID, step.StepID)] = &record
	// 每个副作用步骤暂停时都写一次 checkpoint，使重启后仍能恢复"停在哪个动作上"。
	// 版本必须走与成功路径同一个分配器：暂停点已后移到执行期，因此这时可能已经有
	// 前序步骤写下的快照，硬编码 1 会与它们冲突。
	version, err := nextCheckpointVersion(s.deps.Checkpoint, plan.RunID)
	if err != nil {
		return err
	}
	if err := s.saveCheckpointLocked(*plan, version); err != nil {
		return err
	}
	s.observe(ctx, application.RuntimeObservation{RunID: plan.RunID, Phase: "approval.wait", WorkerID: step.WorkerID, ToolID: step.ToolID})
	return s.emitLocked(plan.RunID, agent.ApprovalRequired, map[string]string{
		"approval_id": approvalID,
		"step_id":     step.StepID,
		"worker_id":   step.WorkerID,
		"tool_id":     step.ToolID,
	})
}

// executeLocked 执行一个已批准步骤；全局锁保证重复 resume 不会并发触发副作用。
// ponytail: v1 单进程全局锁，吞吐上限是串行 run；进入多租户或高并发前再按 run_id 分片。
func (s *Service) executeLocked(ctx context.Context, plan agent.ExecutionPlan) error {
	if agent.IsTerminal(plan.Status) {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorToolTimeout, "能力执行超时")
		}
		return s.terminateLocked(&plan, agent.RunCanceled, agent.ErrorCanceled, "执行已取消")
	}
	budget := s.budget()
	if budget.MaxPlanSteps <= 0 || plan.MaxSteps > budget.MaxPlanSteps || len(plan.Steps) > budget.MaxPlanSteps {
		s.observe(ctx, application.RuntimeObservation{RunID: plan.RunID, Phase: "budget.exhausted", ErrorCode: string(agent.ErrorBudgetExceeded)})
		return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorBudgetExceeded, "执行步骤超过预算")
	}
	stepIndex := nextStepIndex(plan)
	if stepIndex < 0 {
		if plan.Status == agent.RunCompleted {
			return s.repairTerminalEventLocked(plan)
		}
		return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorInvalidState, "执行计划没有可推进步骤")
	}
	step := &plan.Steps[stepIndex]
	if step.Status == agent.StepWaitingReconciliation {
		return &Error{Code: agent.ErrorOutcomeUnknown, Message: "外部执行结果待人工核对"}
	}
	// 动作级审批：暂停点在这一步的输入已确定之后。只读步骤不进入这里（FR-003）。
	paused, err := s.ensureStepApprovalLocked(ctx, &plan, step)
	if err != nil {
		return err
	}
	if paused {
		// 本次推进已为该步骤建立审批请求并停在 waiting_approval；
		// 操作者批准后由 Resume 重新进入本函数。
		return nil
	}
	if step.ApprovalRequired {
		// 已持有绑定该动作的批准。审批是操作者等待，不是 Tool 执行：刷新执行窗口，
		// 避免慢审批吃掉有界的 Worker/Tool deadline。
		if plan.Deadline.IsZero() || !s.now().Before(plan.Deadline) {
			plan.Deadline = s.now().Add(budget.Timeout)
			if err := s.savePlanLocked(plan); err != nil {
				return err
			}
		}
	}
	if !plan.Deadline.IsZero() && !s.now().Before(plan.Deadline) {
		return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorToolTimeout, "能力执行超时")
	}
	var memories []conversation.MemoryEntry
	if s.deps.MemoryRead != nil {
		request, found, err := s.loadRequest(plan.RunID)
		if err != nil {
			return err
		}
		if !found {
			return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorInternal, "执行请求不存在")
		}
		scope := conversation.MemoryScope{TenantID: request.Subject.TenantID, SubjectID: request.Subject.SubjectID, ConversationID: request.ConversationID, Kind: conversation.MemoryWorking}
		memories, err = s.deps.MemoryRead.Query(ctx, conversation.MemoryQuery{Scope: scope, Limit: 10, MaxBytes: 4096, Now: s.now()})
		if err != nil {
			return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorInternal, "会话记忆读取失败")
		}
	}
	if s.deps.ToolValidator != nil {
		if err := s.deps.ToolValidator.ValidateInput(step.ToolID, step.Input); err != nil {
			return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorInvalidOutput, "能力输入未通过结构校验")
		}
	}
	if err := plan.TransitionStep(step.StepID, agent.StepRunning); err != nil {
		return err
	}
	plan.ErrorClass = ""
	step.ErrorClass = ""
	attemptLimit := budget.MaxToolCalls
	if budget.CostBudget < attemptLimit {
		attemptLimit = budget.CostBudget
	}
	totalAttempts := planAttempts(plan)
	if attemptLimit <= 0 || totalAttempts >= attemptLimit {
		s.observe(ctx, application.RuntimeObservation{RunID: plan.RunID, WorkerID: step.WorkerID, ToolID: step.ToolID, Phase: "budget.exhausted", ErrorCode: string(agent.ErrorBudgetExceeded), Attempt: totalAttempts})
		return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorBudgetExceeded, "能力调用超过预算")
	}
	step.Attempts++
	// Persist a pre-call marker before the checkpoint. If the process exits here,
	// recovery can safely put the step back to pending because no Runner call was
	// started yet.
	step.AttemptStatus = "prepared"
	if err := s.savePlanLocked(plan); err != nil {
		return err
	}
	version, err := nextCheckpointVersion(s.deps.Checkpoint, plan.RunID)
	if err != nil {
		return err
	}
	if err := s.saveCheckpointLocked(plan, version); err != nil {
		return err
	}
	checkpoint, found, err := s.loadCheckpoint(plan.RunID)
	if err != nil {
		return err
	}
	resumeToken := ""
	if found {
		resumeToken = checkpoint.RunnerToken
	}
	if err := s.emitLocked(plan.RunID, agent.Progress, map[string]string{"status": "running", "step_id": step.StepID}); err != nil {
		return err
	}
	if err := s.emitLocked(plan.RunID, agent.ToolCall, map[string]string{
		"step_id": step.StepID, "worker_id": step.WorkerID, "tool_id": step.ToolID,
	}); err != nil {
		return err
	}
	// The external call is now the next operation. Persisting this marker just
	// before Runner.Run lets recovery distinguish a pre-call crash (prepared)
	// from an in-flight call whose outcome must be reconciled.
	step.AttemptStatus = "running"
	if err := s.savePlanLocked(plan); err != nil {
		return err
	}
	toolCtx := ctx
	cancel := func() {}
	if !plan.Deadline.IsZero() {
		toolCtx, cancel = context.WithDeadline(ctx, plan.Deadline)
	}
	toolCtx, cancelRun := context.WithCancel(toolCtx)
	control := &runControl{cancel: func() { cancelRun(); cancel() }, done: make(chan struct{})}
	s.registerRun(plan.RunID, control)
	runner := s.workerRunner()
	if runner == nil {
		close(control.done)
		s.unregisterRun(plan.RunID, control)
		cancelRun()
		cancel()
		return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorInternal, "Worker Runner 未装配")
	}
	workerStarted := time.Now()
	s.observe(toolCtx, application.RuntimeObservation{RunID: plan.RunID, WorkerID: step.WorkerID, ToolID: step.ToolID, Phase: "worker.start", Attempt: step.Attempts})
	s.observe(toolCtx, application.RuntimeObservation{RunID: plan.RunID, WorkerID: step.WorkerID, ToolID: step.ToolID, Phase: "tool.start", Attempt: step.Attempts})
	// 外部 Runner 不得持有状态锁；Cancel 可以在此期间发出协作式取消。
	s.mu.Unlock()
	workerResult, err := runner.Run(toolCtx, application.WorkerRequest{
		RunID: plan.RunID,
		Session: application.RunnerSession{
			RunID: plan.RunID, Checkpoint: plan.RunID + ":" + step.StepID,
			Deadline: plan.Deadline, ToolBudget: attemptLimit - totalAttempts, ModelBudget: budget.MaxToolCalls,
		},
		Step:           *step,
		ToolBudget:     attemptLimit - totalAttempts,
		ModelBudget:    budget.MaxToolCalls,
		ResumeToken:    resumeToken,
		WorkerID:       step.WorkerID,
		Intent:         step.Intent,
		ToolID:         step.ToolID,
		Input:          cloneMap(step.Input),
		IdempotencyKey: step.IdempotencyKey,
		Deadline:       plan.Deadline,
		Memories:       cloneMemories(memories),
	})
	observedCtxErr := toolCtx.Err()
	s.mu.Lock()
	close(control.done)
	s.unregisterRun(plan.RunID, control)
	cancelRun()
	cancel()
	if err != nil {
		s.observe(toolCtx, application.RuntimeObservation{RunID: plan.RunID, WorkerID: step.WorkerID, ToolID: step.ToolID, Phase: "tool.error", ErrorCode: string(agent.ErrorInternal), ErrorClass: string(classifyRuntimeError(err)), Attempt: step.Attempts, Duration: time.Since(workerStarted)})
		policy := DefaultErrorPolicy{}
		class := policy.Classify(err, application.PhaseInFlight)
		if errors.Is(err, application.ErrOutcomeUnknown) {
			return s.markOutcomeUnknownLocked(&plan, step)
		}
		if class == application.ErrorCanceled || errors.Is(observedCtxErr, context.Canceled) {
			return s.terminateLocked(&plan, agent.RunCanceled, agent.ErrorCanceled, "执行已取消")
		}
		if class == application.ErrorTimeout || errors.Is(observedCtxErr, context.DeadlineExceeded) || (!plan.Deadline.IsZero() && !s.now().Before(plan.Deadline)) {
			return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorToolTimeout, "能力执行超时")
		}
		var preCall *application.PreCallError
		if errors.As(err, &preCall) {
			retryLimit := s.deps.RetryLimitFor(step.ToolID)
			if policy.Classify(err, application.PhasePreCall) == application.ErrorPreCallFailure && policy.Decide(application.ErrorPreCallFailure, step.Attempts, retryLimit) == application.Retry && totalAttempts < attemptLimit {
				s.observe(toolCtx, application.RuntimeObservation{RunID: plan.RunID, WorkerID: step.WorkerID, ToolID: step.ToolID, Phase: "retry", RetryDecision: string(application.Retry), Attempt: step.Attempts, Duration: time.Since(workerStarted)})
				step.AttemptStatus = "failed_pre_call"
				step.ErrorClass = agent.ClassPreCallFailure
				plan.ErrorClass = agent.ClassPreCallFailure
				if err := s.savePlanLocked(plan); err != nil {
					return err
				}
				if err := s.emitLocked(plan.RunID, agent.Progress, map[string]string{"status": "retrying", "step_id": step.StepID}); err != nil {
					return err
				}
				// A pre-call failure never entered the external system, so the step
				// can safely return to pending for the next bounded attempt.
				step.Status = agent.StepPending
				plan.Status = agent.RunPending
				if err := s.savePlanLocked(plan); err != nil {
					return err
				}
				return s.executeLocked(ctx, plan)
			}
			return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorBudgetExceeded, "能力重试超过预算")
		}
		code := agent.ErrorInternal
		if class == application.ErrorUnavailable || class == application.ErrorBusiness {
			code = agent.ErrorInternal
		}
		return s.terminateLocked(&plan, agent.RunFailed, code, "能力执行失败")
	}
	result := workerResult.Content
	s.observe(toolCtx, application.RuntimeObservation{RunID: plan.RunID, WorkerID: step.WorkerID, ToolID: step.ToolID, Phase: "tool.success", Attempt: step.Attempts, Duration: time.Since(workerStarted)})
	if observedCtxErr != nil {
		return s.markOutcomeUnknownLocked(&plan, step)
	}
	if s.deps.ToolValidator != nil {
		if err := s.deps.ToolValidator.ValidateOutput(step.ToolID, result); err != nil {
			return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorInvalidOutput, "能力输出未通过结构校验")
		}
	}
	if err := guardToolResult(result); err != nil {
		return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorInvalidOutput, "能力输出未通过安全校验")
	}
	if err := plan.TransitionStep(step.StepID, agent.StepSucceeded); err != nil {
		return err
	}
	step.AttemptStatus = "succeeded"
	step.ErrorClass = ""
	plan.ErrorClass = ""
	step.ResultContent = result
	digest := sha256.Sum256([]byte(result))
	step.ResultDigest = hex.EncodeToString(digest[:])
	if err := s.savePlanLocked(plan); err != nil {
		return s.markOutcomeUnknownLocked(&plan, step)
	}
	version, err = nextCheckpointVersion(s.deps.Checkpoint, plan.RunID)
	if err != nil {
		return err
	}
	if err := s.saveCheckpointLocked(plan, version, workerResult.Checkpoint); err != nil {
		return err
	}
	if err := s.emitLocked(plan.RunID, agent.Progress, map[string]string{"status": "succeeded", "step_id": step.StepID}); err != nil {
		return err
	}
	if err := s.emitLocked(plan.RunID, agent.Text, map[string]string{"content": result, "step_id": step.StepID}); err != nil {
		return err
	}
	if plan.Status == agent.RunCompleted {
		return s.emitLocked(plan.RunID, agent.Completed, map[string]string{"message": "执行完成"})
	}
	// A successful step is durable before the next step is selected. The
	// manager, not the Runner, owns ordered progression and terminal projection.
	if next := nextStepIndex(plan); next >= 0 && next < len(plan.Steps) && (plan.Steps[next].WorkerID != step.WorkerID || plan.Steps[next].ToolID != step.ToolID) {
		// Bounded handoff: only the prior result becomes the next step's message,
		// capped before it reaches the next registered Tool.
		plan.Steps[next].Input = map[string]string{"message": boundedHandoff(result)}
	}
	plan.Status = agent.RunPending
	if err := s.savePlanLocked(plan); err != nil {
		return err
	}
	return s.executeLocked(ctx, plan)
}

func decisionCandidates(decision agent.SupervisorDecision) []agent.CandidateStep {
	if len(decision.Steps) > 0 {
		return append([]agent.CandidateStep(nil), decision.Steps...)
	}
	return []agent.CandidateStep{{WorkerID: decision.WorkerID, Intent: decision.Intent, Arguments: cloneMap(decision.Arguments), Risk: decision.Risk}}
}

func decisionStepCount(decision agent.SupervisorDecision) int {
	if len(decision.Steps) > 0 {
		return len(decision.Steps)
	}
	return 1
}

// toolInput keeps route metadata out of the Tool payload. Policy evaluates the
// full candidate, while the approved Tool receives only its declared inputs.
func toolInput(arguments map[string]string) map[string]string {
	input := make(map[string]string, len(arguments))
	for key, value := range arguments {
		if key != "tool_id" {
			input[key] = value
		}
	}
	return input
}

func boundedHandoff(value string) string {
	const max = 512
	if len(value) <= max {
		return value
	}
	return value[:max]
}

// markOutcomeUnknownLocked records that the external call started but its result
// could not be durably committed. It deliberately never retries the Runner.
func (s *Service) markOutcomeUnknownLocked(plan *agent.ExecutionPlan, step *agent.PlanStep) error {
	if step.Status != agent.StepWaitingReconciliation {
		// The durable write may have failed after the external call. At this
		// point the in-memory plan is intentionally forced into reconciliation.
		step.Status = agent.StepWaitingReconciliation
		plan.Status = agent.RunWaitingReconciliation
	}
	step.AttemptStatus = "unknown"
	step.ErrorClass = agent.ClassUnknownOutcome
	plan.ErrorClass = agent.ClassUnknownOutcome
	var persistErr error
	if err := s.savePlanLocked(*plan); err != nil {
		persistErr = errors.Join(persistErr, fmt.Errorf("保存未知结果计划失败: %w", err))
	}
	if version, err := nextCheckpointVersion(s.deps.Checkpoint, plan.RunID); err == nil {
		if err := s.saveCheckpointLocked(*plan, version); err != nil {
			persistErr = errors.Join(persistErr, fmt.Errorf("保存未知结果 checkpoint 失败: %w", err))
		}
	} else {
		persistErr = errors.Join(persistErr, fmt.Errorf("读取未知结果 checkpoint 版本失败: %w", err))
	}
	if err := s.emitLocked(plan.RunID, agent.ReconciliationRequired, map[string]string{
		"code": string(agent.ErrorOutcomeUnknown), "message": "外部执行结果待人工核对",
	}); err != nil {
		persistErr = errors.Join(persistErr, fmt.Errorf("记录未知结果事件失败: %w", err))
	}
	unknown := &Error{Code: agent.ErrorOutcomeUnknown, Message: "外部执行结果待人工核对"}
	if persistErr != nil {
		unknown.cause = persistErr
	}
	return unknown
}

func (s *Service) repairTerminalEventLocked(plan agent.ExecutionPlan) error {
	for _, event := range s.deps.EventBus.Events(plan.RunID) {
		if event.Type == agent.Completed || event.Type == agent.Failed || event.Type == agent.Canceled {
			return nil
		}
	}
	if plan.Status == agent.RunCompleted {
		return s.emitLocked(plan.RunID, agent.Completed, map[string]string{"message": "执行完成"})
	}
	if plan.Status == agent.RunCanceled {
		return s.emitLocked(plan.RunID, agent.Canceled, map[string]string{"code": string(plan.TerminalCode), "message": "执行已取消"})
	}
	return s.emitLocked(plan.RunID, agent.Failed, map[string]string{"code": string(plan.TerminalCode), "message": "执行失败"})
}

// reconcileExpiredInFlightLocked converts an in-flight snapshot left by a
// crashed owner into an explicit manual-reconciliation state. A new owner only
// reaches this method after claiming the Run lease, so it never retries an
// external call whose outcome is unknown.
func (s *Service) reconcileExpiredInFlightLocked(plan *agent.ExecutionPlan) error {
	if plan == nil || agent.IsTerminal(plan.Status) {
		return nil
	}
	changed := false
	unknown := false
	for index := range plan.Steps {
		step := &plan.Steps[index]
		if step.Status != agent.StepRunning {
			continue
		}
		if step.AttemptStatus == "prepared" {
			if err := plan.TransitionStep(step.StepID, agent.StepPending); err != nil {
				return err
			}
			if step.Attempts > 0 {
				step.Attempts--
			}
			step.AttemptStatus = "recovered_pre_call"
			step.ErrorClass = ""
			changed = true
			continue
		}
		if err := plan.TransitionStep(step.StepID, agent.StepWaitingReconciliation); err != nil {
			return err
		}
		step.AttemptStatus = "unknown"
		step.ErrorClass = agent.ClassUnknownOutcome
		unknown = true
		changed = true
	}
	if !changed {
		return nil
	}
	if !unknown {
		plan.Status = agent.RunPending
		plan.ErrorClass = ""
		if err := s.savePlanLocked(*plan); err != nil {
			return err
		}
		version, err := nextCheckpointVersion(s.deps.Checkpoint, plan.RunID)
		if err != nil {
			return err
		}
		return s.saveCheckpointLocked(*plan, version)
	}
	plan.ErrorClass = agent.ClassUnknownOutcome
	if err := s.savePlanLocked(*plan); err != nil {
		return err
	}
	version, err := nextCheckpointVersion(s.deps.Checkpoint, plan.RunID)
	if err != nil {
		return err
	}
	if err := s.saveCheckpointLocked(*plan, version); err != nil {
		return err
	}
	return s.emitLocked(plan.RunID, agent.ReconciliationRequired, map[string]string{
		"code": string(agent.ErrorOutcomeUnknown), "message": "进程恢复时发现未确认的外部执行结果",
	})
}

func (s *Service) repairStepProjectionsLocked(plan agent.ExecutionPlan) error {
	events := s.deps.EventBus.Events(plan.RunID)
	for _, step := range plan.Steps {
		if step.Status != agent.StepSucceeded {
			continue
		}
		progressFound, textFound := false, false
		for _, event := range events {
			if event.Type == agent.Progress && event.Data["status"] == "succeeded" && event.Data["step_id"] == step.StepID {
				progressFound = true
			}
			if event.Type == agent.Text && event.Data["step_id"] == step.StepID {
				textFound = true
			}
		}
		if !progressFound {
			if err := s.emitLocked(plan.RunID, agent.Progress, map[string]string{"status": "succeeded", "step_id": step.StepID}); err != nil {
				return err
			}
		}
		if !textFound {
			if err := s.emitLocked(plan.RunID, agent.Text, map[string]string{"content": step.ResultContent, "step_id": step.StepID}); err != nil {
				return err
			}
		}
		events = s.deps.EventBus.Events(plan.RunID)
	}
	return nil
}

// workerRunner 返回启动阶段装配的唯一执行合同。
func (s *Service) workerRunner() application.WorkerRunner {
	return s.deps.Runner
}

func (s *Service) observe(ctx context.Context, observation application.RuntimeObservation) {
	if s != nil && s.deps.Observer != nil {
		s.deps.Observer.Observe(ctx, observation)
	}
}

func classifyRuntimeError(err error) application.ErrorClass {
	if err == nil {
		return ""
	}
	return (DefaultErrorPolicy{}).Classify(err, application.PhaseInFlight)
}

func (s *Service) budget() application.ExecutionBudget {
	budget := s.deps.Budget
	if budget.MaxPlanSteps <= 0 {
		budget.MaxPlanSteps = 1
	}
	if budget.MaxToolCalls <= 0 {
		budget.MaxToolCalls = 1
	}
	if budget.CostBudget <= 0 {
		budget.CostBudget = budget.MaxToolCalls
	}
	if budget.Timeout <= 0 {
		budget.Timeout = 5 * time.Second
	}
	return budget
}

func (s *Service) registerRun(runID string, control *runControl) {
	s.controlMu.Lock()
	s.active[runID] = control
	s.controlMu.Unlock()
}

func (s *Service) unregisterRun(runID string, control *runControl) {
	s.controlMu.Lock()
	if s.active[runID] == control {
		delete(s.active, runID)
	}
	s.controlMu.Unlock()
}

func (s *Service) activeRun(runID string) *runControl {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	return s.active[runID]
}

// authorizeRun 从持久化请求读取 run 所有者，避免仅凭 run_id 暴露或控制执行。
func (s *Service) authorizeRun(ctx context.Context, subject identity.Subject, runID string) error {
	request, found, err := s.loadRequest(runID)
	if err != nil {
		return err
	}
	if !found {
		return &Error{Code: agent.ErrorAccessDenied, Message: "无权访问此执行"}
	}
	return s.authorize(ctx, subject, request.Subject)
}

// authorize 将用户资源授权与模型路由后的 Policy Gate 保持为两个独立关口。
func (s *Service) authorize(ctx context.Context, subject, owner identity.Subject) error {
	if !subject.Valid() {
		return &Error{Code: agent.ErrorUnauthenticated, Message: "身份凭证无效"}
	}
	if s.deps.RunAuth == nil {
		return &Error{Code: agent.ErrorInternal, Message: "访问控制未装配"}
	}
	if err := s.deps.RunAuth.Authorize(ctx, subject, owner); err != nil {
		if errors.Is(err, application.ErrAccessDenied) {
			return &Error{Code: agent.ErrorAccessDenied, Message: "无权访问此执行"}
		}
		return err
	}
	return nil
}

// saveCheckpointLocked 保存恢复所需的最小快照。
func (s *Service) saveCheckpointLocked(plan agent.ExecutionPlan, version int64, runnerToken ...string) error {
	if err := s.ensureCurrentLease(); err != nil {
		return err
	}
	status := plan.Status
	returnValue := agent.Checkpoint{RunID: plan.RunID, PlanID: plan.PlanID, Status: status, Version: version, SavedAt: s.now()}
	if previous, found, err := s.loadCheckpoint(plan.RunID); err != nil {
		return err
	} else if found {
		returnValue.RunnerToken = previous.RunnerToken
	}
	if len(runnerToken) > 0 {
		returnValue.RunnerToken = runnerToken[0]
	}
	for _, step := range plan.Steps {
		if step.Status == agent.StepSucceeded {
			returnValue.CompletedStepIDs = append(returnValue.CompletedStepIDs, step.StepID)
		}
		if step.Status != agent.StepSucceeded {
			returnValue.NextStepID = step.StepID
			break
		}
	}
	var err error
	if store, ok := s.deps.Checkpoint.(application.ContextCheckpointStore); ok {
		_, err = store.SaveContext(s.operationContext(), returnValue)
	} else {
		_, err = s.deps.Checkpoint.Save(returnValue)
	}
	return err
}

// expireApprovalLocked 把超时的待审批记录标记为过期，并以失败收尾该 Run。
//
// 审批超时与审批被拒都收敛到 POLICY_DENIED 终态；两者的区别保留在审批记录的
// Status 上（expired / rejected），审计不依赖错误码区分。
func (s *Service) expireApprovalLocked(plan *agent.ExecutionPlan, request *approval.Request) error {
	_ = request.Decide(approval.Expired, "", s.now())
	if err := s.ensureCurrentLease(); err != nil {
		return err
	}
	if err := s.deps.Repository.SaveApproval(*request); err != nil {
		return err
	}
	s.observe(s.operationContext(), application.RuntimeObservation{RunID: plan.RunID, Phase: "approval.expired"})
	if err := s.terminateLocked(plan, agent.RunFailed, agent.ErrorPolicyDenied, "审批已超时"); err != nil {
		if _, ok := err.(*Error); !ok {
			return err
		}
	}
	return nil
}

// failLocked 记录统一终态，避免错误分支各自拼接公开消息。
func (s *Service) failLocked(runID, code, message string) error {
	return s.emitLocked(runID, agent.Failed, map[string]string{"code": code, "message": message})
}

// terminateLocked 收口失败或取消分支，确保计划、checkpoint 和终态事件一致。
func (s *Service) terminateLocked(plan *agent.ExecutionPlan, status agent.RunStatus, code agent.ErrorCode, message string) error {
	previousContext := s.ctx
	s.ctx = context.WithoutCancel(s.operationContext())
	defer func() { s.ctx = previousContext }()
	terminalStepID := ""
	class := errorClassForCode(code)
	plan.ErrorClass = class
	if !agent.IsTerminal(plan.Status) && len(plan.Steps) > 0 {
		stepIndex := nextStepIndex(*plan)
		if stepIndex < 0 {
			return plan.MarkTerminal(status, code)
		}
		step := &plan.Steps[stepIndex]
		terminalStepID = step.StepID
		if step.Status != agent.StepSucceeded && step.Status != agent.StepFailed && step.Status != agent.StepCanceled {
			next := agent.StepFailed
			if status == agent.RunCanceled {
				next = agent.StepCanceled
			}
			if err := plan.TransitionStep(step.StepID, next); err != nil {
				return err
			}
		}
		step.ErrorClass = class
	}
	if err := plan.MarkTerminal(status, code); err != nil {
		return err
	}
	if err := s.savePlanLocked(*plan); err != nil {
		return err
	}
	version, err := nextCheckpointVersion(s.deps.Checkpoint, plan.RunID)
	if err != nil {
		return err
	}
	if err := s.saveCheckpointLocked(*plan, version); err != nil {
		return err
	}
	eventType := agent.Failed
	if status == agent.RunCanceled {
		eventType = agent.Canceled
	}
	terminalData := map[string]string{"code": string(code), "message": message}
	if terminalStepID != "" {
		terminalData["step_id"] = terminalStepID
	}
	if err := s.emitLocked(plan.RunID, eventType, terminalData); err != nil {
		return err
	}
	return &Error{Code: code, Message: message}
}

func errorClassForCode(code agent.ErrorCode) agent.ErrorClass {
	switch code {
	case agent.ErrorCanceled:
		return agent.ClassCanceled
	case agent.ErrorToolTimeout:
		return agent.ClassTimeout
	case agent.ErrorInvalidOutput:
		return agent.ClassInvalidOutput
	case agent.ErrorPolicyDenied, agent.ErrorUnknownCapability:
		return agent.ClassPolicyDenied
	case agent.ErrorOutcomeUnknown:
		return agent.ClassUnknownOutcome
	default:
		return agent.ClassInternal
	}
}

// emitLocked 按已持久化事件生成稳定 ID；事件顺序由 EventStore 再次校验。
func (s *Service) emitLocked(runID string, eventType agent.EventType, data map[string]string) error {
	if err := s.ensureCurrentLease(); err != nil {
		return err
	}
	ctx := s.operationContext()
	events := s.deps.EventBus.Events(runID)
	if store, ok := s.deps.EventBus.(application.ContextEventStore); ok {
		events = store.EventsContext(ctx, runID)
	}
	eventID := fmt.Sprintf("%s:event:%d", runID, len(events)+1)
	event := agent.RunEvent{EventID: eventID, RunID: runID, Type: eventType, OccurredAt: s.now(), Data: cloneMap(data), RedactionClass: "public"}
	if store, ok := s.deps.EventBus.(application.ContextEventStore); ok {
		_, err := store.AppendContext(ctx, event)
		return err
	}
	_, err := s.deps.EventBus.Append(event)
	return err
}

// newRunID 生成单进程稳定 ID；run_id 只作关联键，不承载业务含义。
func (s *Service) newRunID() string {
	return fmt.Sprintf("run-%d-%d", s.now().UnixNano(), s.sequence.Add(1))
}

func nextCheckpointVersion(store application.CheckpointStore, runID string) (int64, error) {
	if reader, ok := store.(application.CheckpointReader); ok {
		snapshot, found, err := reader.GetWithError(runID)
		if err != nil {
			return 0, err
		}
		if found {
			return snapshot.Version + 1, nil
		}
		return 1, nil
	}
	if snapshot, ok := store.Get(runID); ok {
		return snapshot.Version + 1, nil
	}
	return 1, nil
}

func (s *Service) loadRequest(runID string) (conversation.ExecutionRequest, bool, error) {
	if reader, ok := s.deps.Repository.(application.RepositoryReader); ok {
		return reader.LoadRequest(runID)
	}
	request, found := s.deps.Repository.GetRequest(runID)
	return request, found, nil
}

func (s *Service) loadApproval(runID, stepID string) (approval.Request, bool, error) {
	if reader, ok := s.deps.Repository.(application.RepositoryReader); ok {
		return reader.LoadApproval(runID, stepID)
	}
	request, found := s.deps.Repository.GetApproval(runID, stepID)
	return request, found, nil
}

func (s *Service) loadPlan(runID string) (agent.ExecutionPlan, bool, error) {
	if reader, ok := s.deps.Repository.(application.RepositoryReader); ok {
		return reader.LoadPlan(runID)
	}
	plan, found := s.deps.Repository.GetPlan(runID)
	return plan, found, nil
}

func (s *Service) loadCheckpoint(runID string) (agent.Checkpoint, bool, error) {
	if reader, ok := s.deps.Checkpoint.(application.CheckpointReader); ok {
		return reader.GetWithError(runID)
	}
	snapshot, found := s.deps.Checkpoint.Get(runID)
	return snapshot, found, nil
}

func classifyPolicyError(err error) agent.ErrorCode {
	if strings.Contains(err.Error(), string(agent.ErrorUnknownCapability)) {
		return agent.ErrorUnknownCapability
	}
	return agent.ErrorPolicyDenied
}

func publicPolicyMessage(code agent.ErrorCode) string {
	if code == agent.ErrorUnknownCapability {
		return "请求引用了未注册能力"
	}
	return "请求未通过策略检查"
}

func cloneMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneMemories(entries []conversation.MemoryEntry) []conversation.MemoryEntry {
	cloned := make([]conversation.MemoryEntry, len(entries))
	for i, entry := range entries {
		cloned[i] = entry
		cloned[i].Metadata = cloneMap(entry.Metadata)
	}
	return cloned
}

func nextStepIndex(plan agent.ExecutionPlan) int {
	for i, step := range plan.Steps {
		if step.Status != agent.StepSucceeded {
			return i
		}
	}
	return -1
}

func planAttempts(plan agent.ExecutionPlan) int {
	total := 0
	for _, step := range plan.Steps {
		total += step.Attempts
	}
	return total
}

// guardToolResult 拒绝把凭证、Prompt 或内部路径送入公开文本事件。
func guardToolResult(result string) error {
	value := strings.ToLower(strings.TrimSpace(result))
	if value == "" {
		return errors.New("能力输出为空")
	}
	if !utf8.ValidString(result) || len(result) > 1<<20 {
		return errors.New("能力输出格式或大小非法")
	}
	for _, char := range result {
		if char == '\u0000' || (char < 0x20 && char != '\n' && char != '\r' && char != '\t') {
			return errors.New("能力输出包含控制字符")
		}
	}
	for _, marker := range []string{
		"api_key=", "apikey=", "authorization:", "bearer ", "password=", "secret=", "sk-",
		"system prompt", "原始 prompt", "prompt:", "/home/", "/workspace/", "internal/", ".env", "file://", "c:\\",
	} {
		if strings.Contains(value, marker) {
			return fmt.Errorf("能力输出包含敏感内容: %s", marker)
		}
	}
	return nil
}
