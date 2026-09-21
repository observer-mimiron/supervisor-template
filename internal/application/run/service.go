// Package run 负责一次执行的生命周期和恢复入口。
//
// 本包是运行状态 owner：负责 Supervisor -> Policy -> Plan -> Tool -> Event 的有界编排；
// 不绑定 Gin、Eino 或具体 Tool 实现，传输层只能读取本包生成的事件。
package run

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/approval"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
)

// Error 是可映射为公开稳定分类的运行错误。
type Error struct {
	Code    agent.ErrorCode
	Message string
}

// Error 返回不包含内部堆栈和凭证的用户可理解消息。
func (e *Error) Error() string { return e.Message }

// Service 是单进程运行状态和 ExecutionPlan 的唯一 owner。
type Service struct {
	mu        sync.Mutex
	deps      application.Dependencies
	approvals map[string]*approval.Request
	sequence  atomic.Uint64
	now       func() time.Time
}

// NewService 创建运行服务；依赖必须由 composition 统一装配。
func NewService(deps application.Dependencies) *Service {
	return &Service{deps: deps, approvals: make(map[string]*approval.Request), now: time.Now}
}

// Start 接收一次用户请求，返回稳定 run_id；重复提交同一 run_id 只重放原事件。
func (s *Service) Start(ctx context.Context, request conversation.ExecutionRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.RunID == "" {
		request.RunID = s.newRunID()
	}
	if request.ConversationID == "" || strings.TrimSpace(request.Message) == "" {
		return request.RunID, &Error{Code: "INVALID_REQUEST", Message: "会话和消息不能为空"}
	}
	if existing := s.deps.EventBus.Events(request.RunID); len(existing) > 0 {
		stored, ok := s.deps.Repository.GetRequest(request.RunID)
		if !ok || stored.ConversationID != request.ConversationID {
			return request.RunID, &Error{Code: agent.ErrorRunNotResumable, Message: "run_id 不属于当前会话"}
		}
		return request.RunID, nil
	}
	if request.RequestedAt.IsZero() {
		request.RequestedAt = s.now()
	}
	if err := s.deps.Repository.SaveRequest(request); err != nil {
		return request.RunID, err
	}
	if err := s.emitLocked(request.RunID, agent.Started, map[string]string{"conversation_id": request.ConversationID}); err != nil {
		return request.RunID, err
	}
	decision, err := s.deps.Supervisor.Decide(ctx, request)
	if err != nil {
		_ = s.failLocked(request.RunID, "INTERNAL_ERROR", "无法生成路由决策")
		return request.RunID, err
	}
	if err := s.emitLocked(request.RunID, agent.Decision, map[string]string{
		"worker_id": decision.WorkerID,
		"tool_id":   decision.Arguments["tool_id"],
	}); err != nil {
		return request.RunID, err
	}
	route, err := s.deps.Policy.Evaluate(decision)
	if err != nil {
		code := classifyPolicyError(err)
		_ = s.failLocked(request.RunID, string(code), publicPolicyMessage(code))
		return request.RunID, &Error{Code: code, Message: publicPolicyMessage(code)}
	}
	step := agent.PlanStep{
		StepID:         request.RunID + ":step-1",
		ToolID:         route.AllowedTools[0],
		Input:          cloneMap(decision.Arguments),
		Status:         agent.StepPending,
		IdempotencyKey: request.RunID + ":step-1",
	}
	plan, err := agent.NewExecutionPlan(request.RunID+":plan", request.RunID, []agent.PlanStep{step}, 1, s.now().Add(5*time.Second))
	if err != nil {
		_ = s.failLocked(request.RunID, "INTERNAL_ERROR", "无法创建执行计划")
		return request.RunID, err
	}
	if err := s.deps.Repository.SavePlan(plan); err != nil {
		return request.RunID, err
	}
	if err := s.emitLocked(request.RunID, agent.Plan, map[string]string{"plan_id": plan.PlanID, "tool_id": step.ToolID}); err != nil {
		return request.RunID, err
	}
	if route.ApprovalRequired {
		if err := plan.TransitionStep(step.StepID, agent.StepWaitingApproval); err != nil {
			return request.RunID, err
		}
		if err := s.deps.Repository.SavePlan(plan); err != nil {
			return request.RunID, err
		}
		approvalRecord := approval.Request{
			ApprovalID:    request.RunID + ":approval",
			RunID:         request.RunID,
			StepID:        step.StepID,
			ActionSummary: "模拟触达示例用户",
			Risk:          string(agent.RiskSideEffect),
			Status:        approval.Pending,
		}
		if err := s.deps.Repository.SaveApproval(approvalRecord); err != nil {
			return request.RunID, err
		}
		s.approvals[request.RunID] = &approvalRecord
		if err := s.saveCheckpointLocked(plan, 1); err != nil {
			return request.RunID, err
		}
		if err := s.emitLocked(request.RunID, agent.ApprovalRequired, map[string]string{"approval_id": request.RunID + ":approval"}); err != nil {
			return request.RunID, err
		}
		return request.RunID, nil
	}
	if err := s.executeLocked(ctx, plan); err != nil {
		return request.RunID, err
	}
	return request.RunID, nil
}

// Approve 写入审批决定；批准只解除门控，实际 Tool 调用由 Resume 推进。
func (s *Service) Approve(_ context.Context, runID, decision, reviewer string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.approvalLocked(runID)
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
	if err := request.Decide(status, reviewer, s.now()); err != nil {
		return &Error{Code: "INVALID_REQUEST", Message: err.Error()}
	}
	if err := s.deps.Repository.SaveApproval(*request); err != nil {
		return err
	}
	if request.Status == approval.Rejected {
		plan, ok := s.deps.Repository.GetPlan(runID)
		if !ok {
			return &Error{Code: agent.ErrorRunNotResumable, Message: "执行计划不存在"}
		}
		if err := s.terminateLocked(&plan, agent.RunFailed, agent.ErrorPolicyDenied, "审批已拒绝"); err != nil {
			if _, ok := err.(*Error); !ok {
				return err
			}
		}
	}
	return nil
}

// Cancel 将未终态执行标记为 canceled，并持久化唯一取消事件。
func (s *Service) Cancel(_ context.Context, runID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.deps.Repository.GetPlan(runID)
	if !ok {
		return &Error{Code: agent.ErrorRunNotResumable, Message: "执行不存在或不可取消"}
	}
	if agent.IsTerminal(plan.Status) {
		return nil
	}
	if request, ok := s.approvalLocked(runID); ok && request.Status == approval.Pending {
		_ = request.Decide(approval.Expired, "", s.now())
		_ = s.deps.Repository.SaveApproval(*request)
	}
	if err := s.terminateLocked(&plan, agent.RunCanceled, agent.ErrorCanceled, "执行已取消"); err != nil {
		if _, ok := err.(*Error); !ok {
			return err
		}
	}
	return nil
}

// Resume 从 checkpoint 继续执行；终态 run 只返回原事件，不再次调用 Tool。
func (s *Service) Resume(ctx context.Context, runID string) ([]agent.RunEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan, ok := s.deps.Repository.GetPlan(runID)
	if !ok {
		return nil, &Error{Code: "RUN_NOT_RESUMABLE", Message: "执行不存在或不可恢复"}
	}
	if agent.IsTerminal(plan.Status) {
		return s.deps.EventBus.Events(runID), nil
	}
	if approvalRequest, ok := s.approvalLocked(runID); ok && approvalRequest.Status == approval.Pending {
		return nil, &Error{Code: "APPROVAL_REQUIRED", Message: "执行仍等待审批"}
	}
	if err := s.executeLocked(ctx, plan); err != nil {
		return s.deps.EventBus.Events(runID), err
	}
	return s.deps.EventBus.Events(runID), nil
}

// Events 返回 run 的事件快照，供 SSE 只读投影。
func (s *Service) Events(runID string) []agent.RunEvent { return s.deps.EventBus.Events(runID) }

// approvalLocked 先读进程缓存，再从 Repository 恢复审批快照，支持重启后的 resume。
func (s *Service) approvalLocked(runID string) (*approval.Request, bool) {
	if request := s.approvals[runID]; request != nil {
		return request, true
	}
	request, ok := s.deps.Repository.GetApproval(runID)
	if !ok {
		return nil, false
	}
	s.approvals[runID] = &request
	return &request, true
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
	if !plan.Deadline.IsZero() && !time.Now().Before(plan.Deadline) {
		return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorToolTimeout, "能力执行超时")
	}
	step := &plan.Steps[0]
	if step.Status == agent.StepWaitingApproval {
		request, ok := s.approvalLocked(plan.RunID)
		if !ok || request.Status != approval.Approved {
			return &Error{Code: "APPROVAL_REQUIRED", Message: "执行仍等待审批"}
		}
	}
	if err := plan.TransitionStep(step.StepID, agent.StepRunning); err != nil {
		return err
	}
	if err := s.deps.Repository.SavePlan(plan); err != nil {
		return err
	}
	if err := s.saveCheckpointLocked(plan, nextCheckpointVersion(s.deps.Checkpoint, plan.RunID)); err != nil {
		return err
	}
	if err := s.emitLocked(plan.RunID, agent.Progress, map[string]string{"status": "running", "step_id": step.StepID}); err != nil {
		return err
	}
	if err := s.emitLocked(plan.RunID, agent.ToolCall, map[string]string{"tool_id": step.ToolID}); err != nil {
		return err
	}
	toolCtx := ctx
	cancel := func() {}
	if !plan.Deadline.IsZero() {
		toolCtx, cancel = context.WithDeadline(ctx, plan.Deadline)
	}
	defer cancel()
	result, err := s.deps.Tools.Execute(toolCtx, step.ToolID, step.Input, step.IdempotencyKey)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(toolCtx.Err(), context.Canceled) {
			return s.terminateLocked(&plan, agent.RunCanceled, agent.ErrorCanceled, "执行已取消")
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(toolCtx.Err(), context.DeadlineExceeded) || (!plan.Deadline.IsZero() && !time.Now().Before(plan.Deadline)) {
			return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorToolTimeout, "能力执行超时")
		}
		return s.terminateLocked(&plan, agent.RunFailed, "TOOL_ERROR", "能力执行失败")
	}
	if err := guardToolResult(result); err != nil {
		return s.terminateLocked(&plan, agent.RunFailed, agent.ErrorInvalidOutput, "能力输出未通过安全校验")
	}
	if err := plan.TransitionStep(step.StepID, agent.StepSucceeded); err != nil {
		return err
	}
	if err := s.deps.Repository.SavePlan(plan); err != nil {
		return err
	}
	if err := s.saveCheckpointLocked(plan, nextCheckpointVersion(s.deps.Checkpoint, plan.RunID)); err != nil {
		return err
	}
	if err := s.emitLocked(plan.RunID, agent.Progress, map[string]string{"status": "succeeded", "step_id": step.StepID}); err != nil {
		return err
	}
	if err := s.emitLocked(plan.RunID, agent.Text, map[string]string{"content": result}); err != nil {
		return err
	}
	return s.emitLocked(plan.RunID, agent.Completed, map[string]string{"message": "执行完成"})
}

// saveCheckpointLocked 保存恢复所需的最小快照。
func (s *Service) saveCheckpointLocked(plan agent.ExecutionPlan, version int64) error {
	status := plan.Status
	returnValue := agent.Checkpoint{RunID: plan.RunID, PlanID: plan.PlanID, Status: status, Version: version, SavedAt: s.now()}
	for _, step := range plan.Steps {
		if step.Status == agent.StepSucceeded {
			returnValue.CompletedStepIDs = append(returnValue.CompletedStepIDs, step.StepID)
		}
		if step.Status != agent.StepSucceeded {
			returnValue.NextStepID = step.StepID
			break
		}
	}
	_, err := s.deps.Checkpoint.Save(returnValue)
	return err
}

// failLocked 记录统一终态，避免错误分支各自拼接公开消息。
func (s *Service) failLocked(runID, code, message string) error {
	return s.emitLocked(runID, agent.Failed, map[string]string{"code": code, "message": message})
}

// terminateLocked 收口失败或取消分支，确保计划、checkpoint 和终态事件一致。
func (s *Service) terminateLocked(plan *agent.ExecutionPlan, status agent.RunStatus, code agent.ErrorCode, message string) error {
	if !agent.IsTerminal(plan.Status) && len(plan.Steps) > 0 {
		step := &plan.Steps[0]
		if step.Status != agent.StepSucceeded && step.Status != agent.StepFailed && step.Status != agent.StepCanceled {
			next := agent.StepFailed
			if status == agent.RunCanceled {
				next = agent.StepCanceled
			}
			if err := plan.TransitionStep(step.StepID, next); err != nil {
				return err
			}
		}
	}
	if err := plan.MarkTerminal(status, code); err != nil {
		return err
	}
	if err := s.deps.Repository.SavePlan(*plan); err != nil {
		return err
	}
	if err := s.saveCheckpointLocked(*plan, nextCheckpointVersion(s.deps.Checkpoint, plan.RunID)); err != nil {
		return err
	}
	eventType := agent.Failed
	if status == agent.RunCanceled {
		eventType = agent.Canceled
	}
	if err := s.emitLocked(plan.RunID, eventType, map[string]string{"code": string(code), "message": message}); err != nil {
		return err
	}
	return &Error{Code: code, Message: message}
}

// emitLocked 生成连续事件 ID；事件顺序由 EventStore 再次校验。
func (s *Service) emitLocked(runID string, eventType agent.EventType, data map[string]string) error {
	_, err := s.deps.EventBus.Append(agent.RunEvent{EventID: fmt.Sprintf("%s:event:%d", runID, s.sequence.Add(1)), RunID: runID, Type: eventType, OccurredAt: s.now(), Data: cloneMap(data), RedactionClass: "public"})
	return err
}

// newRunID 生成单进程稳定 ID；run_id 只作关联键，不承载业务含义。
func (s *Service) newRunID() string {
	return fmt.Sprintf("run-%d-%d", s.now().UnixNano(), s.sequence.Add(1))
}

func nextCheckpointVersion(store application.CheckpointStore, runID string) int64 {
	if snapshot, ok := store.Get(runID); ok {
		return snapshot.Version + 1
	}
	return 1
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

// guardToolResult 拒绝把凭证、Prompt 或内部路径送入公开文本事件。
func guardToolResult(result string) error {
	value := strings.ToLower(strings.TrimSpace(result))
	if value == "" {
		return errors.New("能力输出为空")
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
