package run

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/approval"
	"github.com/observer-mimiron/supervisor-template/internal/domain/operation"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
)

// TestApprovalTimeoutExpiresPendingRun 证明 approval.timeout 真的生效：待审批 Run
// 不会无限期停在 waiting_approval，而是被判定为过期并以失败收尾。
func TestApprovalTimeoutExpiresPendingRun(t *testing.T) {
	service, tools := newTestService()
	service.deps.ApprovalTimeout = time.Minute
	now := time.Now()
	service.now = func() time.Time { return now }

	runID, err := service.Start(context.Background(), request("run-approval-expiry", "模拟触达"))
	if err != nil {
		t.Fatal(err)
	}
	waiting, ok := service.deps.Repository.GetPlan(runID)
	if !ok || waiting.Status != agent.RunWaitingApproval {
		t.Fatalf("run should wait for approval, got %#v", waiting)
	}
	// 审批按步骤绑定：在 Run 仍然等待审批时记下步骤，之后按同一 (run, step) 读记录。
	stepID := pendingApprovalStepID(waiting)
	if stepID == "" {
		t.Fatal("等待审批的 Run 必须有一个处于 waiting_approval 的步骤")
	}

	// 超过配置的等待上限后再推进。
	service.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := service.Resume(context.Background(), testSubject(), runID); err == nil {
		t.Fatal("expired approval must not resume successfully")
	}

	plan, _ := service.deps.Repository.GetPlan(runID)
	if plan.Status != agent.RunFailed {
		t.Fatalf("expired approval should fail the run, got %s", plan.Status)
	}
	record, ok := service.deps.Repository.GetApproval(runID, stepID)
	if !ok || record.Status != approval.Expired {
		t.Fatalf("approval record should be expired, got %#v", record)
	}
	if tools.OutreachCount() != 0 {
		t.Fatalf("expired approval must not execute the side effect, writes=%d", tools.OutreachCount())
	}
	if err := terminalEventCount(service, runID); err != 1 {
		t.Fatalf("expired run must keep exactly one terminal event, got %d", err)
	}
}

// TestApprovalBeforeDeadlineStillWorks 是对照组：没有超时就不该被误判为过期。
func TestApprovalBeforeDeadlineStillWorks(t *testing.T) {
	service, tools := newTestService()
	service.deps.ApprovalTimeout = time.Hour
	now := time.Now()
	service.now = func() time.Time { return now }

	runID, err := service.Start(context.Background(), request("run-approval-in-time", "模拟触达"))
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(time.Minute) }
	if err := service.Approve(context.Background(), testSubject(), runID, "", "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	if tools.OutreachCount() != 1 {
		t.Fatalf("approved run should write once, got %d", tools.OutreachCount())
	}
}

// TestPerToolRetryLimitOverridesGlobalBudget 证明 [tools.*].max_retries 真的参与
// 重试决策，而不是被全局 limits.max_retries 覆盖。
func TestPerToolRetryLimitOverridesGlobalBudget(t *testing.T) {
	service, _ := newTestService()
	service.deps.Budget = application.ExecutionBudget{MaxPlanSteps: 2, MaxToolCalls: 8, MaxRetries: 5, CostBudget: 8, Timeout: time.Minute}
	service.deps.ToolRetryLimits = map[string]int{examplebusiness.ReadOnlyToolID: 0}

	if got := service.deps.RetryLimitFor(examplebusiness.ReadOnlyToolID); got != 0 {
		t.Fatalf("per-tool limit should win, got %d", got)
	}
	if got := service.deps.RetryLimitFor(examplebusiness.SummaryToolID); got != 5 {
		t.Fatalf("unlisted tool should fall back to the global budget, got %d", got)
	}
}

// TestPlanStepCapComesFromBudget 证明候选步骤上限来自 limits.max_plan_steps，
// 而不是写死的数字：同一个两句串行请求在 1 步预算下被拒、在 2 步预算下通过。
func TestPlanStepCapComesFromBudget(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		maxSteps   int
		wantFailed bool
	}{
		{name: "budget of one rejects a two step plan", maxSteps: 1, wantFailed: true},
		{name: "budget of two accepts a two step plan", maxSteps: 2, wantFailed: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, _ := newTestService()
			service.deps.Budget = application.ExecutionBudget{MaxPlanSteps: testCase.maxSteps, MaxToolCalls: 8, MaxRetries: 1, CostBudget: 8, Timeout: time.Minute}
			// 串行示例会引用 user_summary，基础装配只注册了 user_analysis。
			service.deps.Policy = agent.NewPolicyGate([]operation.WorkerContract{
				{WorkerID: examplebusiness.WorkerID, AllowedTools: []string{examplebusiness.ReadOnlyToolID, examplebusiness.SideEffectToolID}},
				{WorkerID: examplebusiness.SummaryWorkerID, AllowedTools: []string{examplebusiness.SummaryToolID}},
			}, examplebusiness.ToolContracts(examplebusiness.ReadOnlyToolFake))

			runID, err := service.Start(context.Background(), request("run-step-cap", "先分析再总结"))
			if testCase.wantFailed {
				if err == nil || !strings.Contains(err.Error(), "上限") {
					t.Fatalf("want budget rejection, got %v", err)
				}
				events := service.Events(runID)
				if len(events) == 0 || events[len(events)-1].Type != agent.Failed {
					t.Fatalf("rejected plan must end in failed, got %#v", events)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			plan, ok := service.deps.Repository.GetPlan(runID)
			if !ok || len(plan.Steps) != 2 || plan.Status != agent.RunCompleted {
				t.Fatalf("two step plan should complete within budget, got %#v", plan)
			}
		})
	}
}

func terminalEventCount(service *Service, runID string) int {
	count := 0
	for _, event := range service.Events(runID) {
		switch event.Type {
		case agent.Completed, agent.Failed, agent.Canceled:
			count++
		}
	}
	return count
}
