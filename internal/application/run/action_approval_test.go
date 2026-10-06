package run

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/approval"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
)

// mixedMessage 触发"第一步只读、第二步副作用"的混合计划。
const mixedMessage = "混合：分析目标客群然后触达"

// newMixedPlanService 返回一个允许两步计划的测试服务。
//
// 默认预算把计划限制在 1 步，混合计划（只读 + 写入）需要显式放开到 2 步。
func newMixedPlanService() (*Service, *examplebusiness.FakeRuntime) {
	service, tools := newTestService()
	service.deps.Budget = application.ExecutionBudget{MaxPlanSteps: 2, MaxToolCalls: 4, MaxRetries: 1, CostBudget: 4, Timeout: time.Minute}
	return service, tools
}

// approvalEvents 返回指定 step_id 的审批事件数量。
func approvalEvents(t *testing.T, service *Service, runID string) map[string]int {
	t.Helper()
	counts := map[string]int{}
	for _, event := range service.Events(runID) {
		if event.Type != agent.ApprovalRequired {
			continue
		}
		counts[event.Data["step_id"]]++
	}
	return counts
}

func toolCallsForStep(t *testing.T, service *Service, runID, stepID string) int {
	t.Helper()
	count := 0
	for _, event := range service.Events(runID) {
		if event.Type == agent.ToolCall && event.Data["step_id"] == stepID {
			count++
		}
	}
	return count
}

// TestMixedPlanBindsApprovalToTheSideEffectStep 是 SC-001 与 US1-1 的判定用例。
//
// 修复前：一次针对只读 step-1 的批准放行了 step-2 的写入，而审批事件通告的是只读
// 那个 tool_id。修复后：审批事件必须指向 step-2 与**写入**那个 Tool，只读步骤不产生
// 审批事件。
func TestMixedPlanBindsApprovalToTheSideEffectStep(t *testing.T) {
	service, tools := newMixedPlanService()
	ctx := context.Background()

	runID, err := service.Start(ctx, request("run-mixed", mixedMessage))
	if err != nil {
		t.Fatal(err)
	}
	plan, ok := service.deps.Repository.GetPlan(runID)
	if !ok || len(plan.Steps) != 2 {
		t.Fatalf("混合计划应有 2 步，得到 %#v", plan.Steps)
	}
	readOnly, sideEffect := plan.Steps[0], plan.Steps[1]
	if readOnly.ToolID != examplebusiness.ReadOnlyToolID || sideEffect.ToolID != examplebusiness.SideEffectToolID {
		t.Fatalf("计划形状不是只读+写入: %#v", plan.Steps)
	}

	// 只读步骤已经执行完，副作用步骤停在审批上。
	counts := approvalEvents(t, service, runID)
	if len(counts) != 1 {
		t.Fatalf("混合计划应只有 1 条审批事件，得到 %#v", counts)
	}
	if counts[sideEffect.StepID] != 1 {
		t.Fatalf("审批事件必须绑定写入步骤 %q，实际 %#v", sideEffect.StepID, counts)
	}
	if counts[readOnly.StepID] != 0 {
		t.Fatalf("只读步骤不得产生审批事件（FR-003），实际 %#v", counts)
	}
	if tools.OutreachCount() != 0 {
		t.Fatalf("批准前不得写入，writes=%d", tools.OutreachCount())
	}
	for _, event := range service.Events(runID) {
		if event.Type == agent.ApprovalRequired && event.Data["tool_id"] != examplebusiness.SideEffectToolID {
			t.Fatalf("审批事件通告的 tool_id 必须是写入那个，得到 %q", event.Data["tool_id"])
		}
	}

	// 审批记录必须绑定该写入步骤，且带有该步骤**最终**输入的指纹。
	record, ok := service.deps.Repository.GetApproval(runID, sideEffect.StepID)
	if !ok {
		t.Fatal("没有找到绑定写入步骤的审批记录")
	}
	if record.StepID != sideEffect.StepID || record.ToolID != examplebusiness.SideEffectToolID {
		t.Fatalf("审批记录绑定了错误的动作: %#v", record)
	}
	// US2-4：审计字段必须齐全。
	if record.RunID != runID || record.Status != approval.Pending || record.RequestedAt.IsZero() {
		t.Fatalf("审批记录缺少审计字段: %#v", record)
	}
	want := approval.ComputeActionDigest(sideEffect.StepID, sideEffect.ToolID, sideEffect.Input)
	if record.ActionDigest == "" || record.ActionDigest != want {
		t.Fatalf("审批记录未绑定该步骤当前输入的指纹: got=%q want=%q", record.ActionDigest, want)
	}
	// 动作摘要来自注册描述，不是应用层文案（FR-005）。
	if record.ActionSummary == "" || record.ActionSummary != sideEffect.ActionSummary {
		t.Fatalf("动作摘要必须来自注册描述: got=%q step=%q", record.ActionSummary, sideEffect.ActionSummary)
	}

	// 批准并恢复后，写入步骤的 tool_call 之前必须存在绑定到它自己的批准。
	if err := service.Approve(ctx, testSubject(), runID, "", "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(ctx, testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	completed, _ := service.deps.Repository.GetPlan(runID)
	if completed.Status != agent.RunCompleted {
		t.Fatalf("批准后应完成，得到 %s", completed.Status)
	}
	if tools.OutreachCount() != 1 {
		t.Fatalf("批准后应写入一次，writes=%d", tools.OutreachCount())
	}
	if toolCallsForStep(t, service, runID, sideEffect.StepID) != 1 {
		t.Fatalf("写入步骤应有一次 tool_call: %#v", service.Events(runID))
	}
}

// TestApprovingOnlyTheReadOnlyStepDoesNotReleaseTheWrite 是 US1-3 的判定用例。
//
// 这条在修复前会红：旧实现把批准固定在 steps[0]（只读步），一次批准即放行写入。
// 修复后只读步骤根本不请求审批，因此"只批准 step-1"这条路不存在；构造等价场景：
// 只读步骤没有可用的批准，副作用步骤必须自己等批准。
func TestApprovingOnlyTheReadOnlyStepDoesNotReleaseTheWrite(t *testing.T) {
	service, tools := newMixedPlanService()
	ctx := context.Background()

	runID, err := service.Start(ctx, request("run-mixed-only-readonly", mixedMessage))
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := service.deps.Repository.GetPlan(runID)
	readOnly, sideEffect := plan.Steps[0], plan.Steps[1]

	// 只读步骤不允许存在任何审批记录。
	if record, ok := service.deps.Repository.GetApproval(runID, readOnly.StepID); ok {
		t.Fatalf("只读步骤不应有审批记录: %#v", record)
	}
	// 直接用只读步骤的标识尝试恢复：既不能放行，也不能产生写入。
	if _, err := service.Resume(ctx, testSubject(), runID); err == nil {
		t.Fatal("未批准写入步骤时 Resume 不应成功")
	}
	if tools.OutreachCount() != 0 {
		t.Fatalf("未批准就执行了副作用，writes=%d", tools.OutreachCount())
	}
	if toolCallsForStep(t, service, runID, sideEffect.StepID) != 0 {
		t.Fatalf("未批准的步骤产生了 tool_call: %#v", service.Events(runID))
	}
	stillWaiting, _ := service.deps.Repository.GetPlan(runID)
	if stillWaiting.Status != agent.RunWaitingApproval {
		t.Fatalf("Run 应仍停在等待审批，得到 %s", stillWaiting.Status)
	}
}

// TestChangedInputRequiresFreshApproval 覆盖 Q1 决策（spec §3.1）。
//
// 审批绑定 (step_id, tool_id, 输入摘要)。参数一旦变化，旧批准不再匹配同一动作，
// 必须重新请求审批且不得执行——否则"批准触达 4 人却执行触达 400 人"无法审计。
func TestChangedInputRequiresFreshApproval(t *testing.T) {
	service, tools := newMixedPlanService()
	ctx := context.Background()

	runID, err := service.Start(ctx, request("run-mixed-param-change", mixedMessage))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(ctx, testSubject(), runID, "", "approve"); err != nil {
		t.Fatal(err)
	}
	plan, _ := service.deps.Repository.GetPlan(runID)
	sideEffect := plan.Steps[1]
	approvedDigest, ok := service.deps.Repository.GetApproval(runID, sideEffect.StepID)
	if !ok || approvedDigest.Status != approval.Approved {
		t.Fatalf("写入步骤应已获批准: %#v", approvedDigest)
	}

	// 模拟"批准之后参数被改写"：直接改计划里该步骤的输入再推进。
	plan.Steps[1].Input = map[string]string{"message": `{"count":400,"customer_ids":[],"spend_365d_total":0}`}
	if err := service.deps.Repository.SavePlan(plan); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Resume(ctx, testSubject(), runID); err == nil {
		t.Fatal("参数变化后 Resume 不应成功")
	}
	if tools.OutreachCount() != 0 {
		t.Fatalf("参数变化后不得沿用旧批准执行，writes=%d", tools.OutreachCount())
	}
	refreshed, ok := service.deps.Repository.GetApproval(runID, sideEffect.StepID)
	if !ok || refreshed.Status != approval.Pending {
		t.Fatalf("参数变化后应重新进入待审批: %#v", refreshed)
	}
	if refreshed.ActionDigest == approvedDigest.ActionDigest {
		t.Fatal("重新请求的审批必须绑定新的输入指纹")
	}
	wantDigest := approval.ComputeActionDigest(sideEffect.StepID, sideEffect.ToolID, plan.Steps[1].Input)
	if refreshed.ActionDigest != wantDigest {
		t.Fatalf("新指纹与当前输入不一致: got=%q want=%q", refreshed.ActionDigest, wantDigest)
	}
	if refreshed.Reviewer != "" || !refreshed.ReviewedAt.IsZero() {
		t.Fatalf("重新请求的审批不得残留上一位批准人: %#v", refreshed)
	}
}

// TestRetryWithSameDigestReusesApproval 覆盖 spec §3.1 的"重试不触发重新审批"。
func TestRetryWithSameDigestReusesApproval(t *testing.T) {
	service, tools := newMixedPlanService()
	ctx := context.Background()

	runID, err := service.Start(ctx, request("run-mixed-retry", mixedMessage))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(ctx, testSubject(), runID, "", "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(ctx, testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	// 重复 Resume：同一动作（同一指纹）沿用原批准，不得再产生审批事件。
	before := len(service.Events(runID))
	if _, err := service.Resume(ctx, testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	if after := len(service.Events(runID)); after != before {
		t.Fatalf("重复 Resume 不得新增事件: before=%d after=%d", before, after)
	}
	if tools.OutreachCount() != 1 {
		t.Fatalf("重复 Resume 不得二次写入，writes=%d", tools.OutreachCount())
	}
	if counts := approvalEvents(t, service, runID); len(counts) != 1 {
		t.Fatalf("重复 Resume 不得重新请求审批: %#v", counts)
	}
}

// TestRejectedApprovalIsNotResurrectedByResume 证明被拒绝的动作不会因为再次推进而复活的。
//
// 若实现把"没有可用的批准"一律当成"重新请求审批"，一次拒绝就能被后续 Resume 洗掉。
func TestRejectedApprovalIsNotResurrectedByResume(t *testing.T) {
	service, tools := newMixedPlanService()
	ctx := context.Background()

	runID, err := service.Start(ctx, request("run-mixed-reject", mixedMessage))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(ctx, testSubject(), runID, "", "reject"); err != nil {
		t.Fatal(err)
	}
	plan, _ := service.deps.Repository.GetPlan(runID)
	if plan.Status != agent.RunFailed {
		t.Fatalf("拒绝后 Run 应以失败收尾，得到 %s", plan.Status)
	}
	// 终态 Run 的 Resume 只重放原事件（既有语义），关键是它不得复活副作用。
	if _, err := service.Resume(ctx, testSubject(), runID); err != nil {
		t.Fatalf("终态 Run 的 Resume 应重放原事件而不是报错: %v", err)
	}
	if tools.OutreachCount() != 0 {
		t.Fatalf("拒绝后不得写入，writes=%d", tools.OutreachCount())
	}
	record, ok := service.deps.Repository.GetApproval(runID, plan.Steps[1].StepID)
	if !ok || record.Status != approval.Rejected {
		t.Fatalf("审批记录应保持 rejected，不得被重新请求洗成 pending: %#v", record)
	}
	if record.Reviewer != testSubject().SubjectID {
		t.Fatalf("拒绝决定必须记录决策人，得到 %q", record.Reviewer)
	}
}

// TestReadOnlyPlanNeverRequestsApproval 证明只读计划完全不进入审批路径（FR-003）。
func TestReadOnlyPlanNeverRequestsApproval(t *testing.T) {
	service, _ := newMixedPlanService()
	ctx := context.Background()

	runID, err := service.Start(ctx, request("run-readonly-no-approval", "分析沉睡客户"))
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := service.deps.Repository.GetPlan(runID)
	if plan.Status != agent.RunCompleted {
		t.Fatalf("只读计划应直接完成，得到 %s", plan.Status)
	}
	if counts := approvalEvents(t, service, runID); len(counts) != 0 {
		t.Fatalf("只读计划不得产生审批事件: %#v", counts)
	}
	for _, step := range plan.Steps {
		if step.ApprovalRequired {
			t.Fatalf("只读步骤被标记为需要审批: %#v", step)
		}
	}
}

// TestApplicationLayerOwnsNoBusinessCopy 守护 SC-005：审批文案只能来自注册描述。
//
// 这里断言应用层产出的摘要与注册描述一致，而不是任何硬编码字符串；另一半证据是
// SC-005 指定的 grep：审批文案不得在 internal/application/ 下出现，逐字判空。
func TestApplicationLayerOwnsNoBusinessCopy(t *testing.T) {
	service, _ := newMixedPlanService()
	ctx := context.Background()

	runID, err := service.Start(ctx, request("run-summary-from-registry", mixedMessage))
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := service.deps.Repository.GetPlan(runID)
	sideEffect := plan.Steps[1]
	record, ok := service.deps.Repository.GetApproval(runID, sideEffect.StepID)
	if !ok {
		t.Fatal("缺少审批记录")
	}
	if strings.TrimSpace(record.ActionSummary) == "" {
		t.Fatal("审批记录缺少动作摘要")
	}
	var registered string
	for _, contract := range examplebusiness.ToolContracts(examplebusiness.ReadOnlyToolFake) {
		if contract.ToolID == examplebusiness.SideEffectToolID {
			registered = contract.Summary
		}
	}
	if registered == "" {
		t.Fatal("注册描述未声明副作用 Tool 的摘要")
	}
	if record.ActionSummary != registered || sideEffect.ActionSummary != registered {
		t.Fatalf("动作摘要必须逐字来自注册描述: record=%q step=%q registered=%q", record.ActionSummary, sideEffect.ActionSummary, registered)
	}
}

// TestApprovalIsScopedToTheRequestedStep 覆盖 US1-2 与接口的步骤维度。
//
// 批准必须指名动作：指到不等待审批的步骤要被拒绝；对同一动作重复批准则是幂等 no-op，
// 既不新增终态事件，也不产生第二次写入。
func TestApprovalIsScopedToTheRequestedStep(t *testing.T) {
	service, tools := newMixedPlanService()
	ctx := context.Background()

	runID, err := service.Start(ctx, request("run-step-scope", mixedMessage))
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := service.deps.Repository.GetPlan(runID)
	readOnly, sideEffect := plan.Steps[0], plan.Steps[1]

	if err := service.Approve(ctx, testSubject(), runID, readOnly.StepID, "approve"); err == nil {
		t.Fatal("批准一个不等待审批的步骤应被拒绝")
	}
	if tools.OutreachCount() != 0 {
		t.Fatalf("被拒绝的批准不得放行写入，writes=%d", tools.OutreachCount())
	}

	if err := service.Approve(ctx, testSubject(), runID, sideEffect.StepID, "approve"); err != nil {
		t.Fatal(err)
	}
	before := len(service.Events(runID))
	if err := service.Approve(ctx, testSubject(), runID, sideEffect.StepID, "approve"); err != nil {
		t.Fatalf("对同一动作重复批准应是幂等 no-op: %v", err)
	}
	if after := len(service.Events(runID)); after != before {
		t.Fatalf("重复批准不得新增事件: before=%d after=%d", before, after)
	}

	if _, err := service.Resume(ctx, testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	if tools.OutreachCount() != 1 {
		t.Fatalf("一次批准只应产生一次写入，writes=%d", tools.OutreachCount())
	}
	if terminalCount(service.Events(runID)) != 1 {
		t.Fatalf("应恰好一个终态事件: %#v", service.Events(runID))
	}
}

// registeredSummary 返回注册描述里某个 Tool 的动作说明。
//
// 用它构造测试步骤，而不是在测试里写业务文案：SC-005 要求
// `internal/application/` 下不出现审批文案，测试同样受这条约束。
func registeredSummary(t *testing.T, toolID string) string {
	t.Helper()
	for _, contract := range examplebusiness.ToolContractsWithMySQL(examplebusiness.ReadOnlyToolFake) {
		if contract.ToolID == toolID {
			return contract.Summary
		}
	}
	t.Fatalf("Tool %q 未在注册描述中声明", toolID)
	return ""
}

// audiencePayload 是假 Tool 接受的客群投影输入。
const audiencePayload = `{"count":4,"customer_ids":["cust-001","cust-002","cust-006","cust-008"],"spend_365d_total":6200}`

// seedPlan 直接写入一份计划并从 Started 事件开始驱动推进。
//
// 用于构造示例业务造不出的计划形状（例如同一计划内的两个副作用步骤）。
func seedPlan(t *testing.T, runID string, steps []agent.PlanStep) (*Service, *examplebusiness.FakeRuntime) {
	t.Helper()
	service, tools := newTestService()
	if err := service.deps.Repository.SaveRequest(request(runID, "test")); err != nil {
		t.Fatal(err)
	}
	plan, err := agent.NewExecutionPlan(runID+":plan", runID, steps, len(steps), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	service.deps.Budget = application.ExecutionBudget{MaxPlanSteps: len(steps), MaxToolCalls: len(steps), MaxRetries: 1, CostBudget: len(steps), Timeout: time.Minute}
	if err := service.deps.Repository.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	err = service.emitLocked(runID, agent.Started, map[string]string{"conversation_id": "demo"})
	service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return service, tools
}

func sideEffectStep(runID, suffix, summary string) agent.PlanStep {
	stepID := runID + ":" + suffix
	return agent.PlanStep{
		StepID: stepID, WorkerID: examplebusiness.WorkerID, Intent: examplebusiness.WorkerID,
		ToolID: examplebusiness.SideEffectToolID, Input: map[string]string{"message": audiencePayload},
		Status: agent.StepPending, ApprovalRequired: true, ActionSummary: summary, IdempotencyKey: stepID,
	}
}

// TestEachSideEffectStepNeedsItsOwnApproval 覆盖 spec §4 的 Edge Case「同一计划内多个副作用步骤」
// （Q2 / R5），同时也是 FR-004「防止一次批准被重放给后续的**同名动作**」的最强形式。
//
// 两个步骤用**同一个 Tool**：如果实现按工具或按 run 匹配批准，第一步的批准会直接放行第二步。
// 只有按 (step_id, tool_id, 输入摘要) 绑定才能把它们分开。
func TestEachSideEffectStepNeedsItsOwnApproval(t *testing.T) {
	ctx := context.Background()
	runID := "run-two-side-effects"
	summary := registeredSummary(t, examplebusiness.SideEffectToolID)
	first, second := sideEffectStep(runID, "step-1", summary), sideEffectStep(runID, "step-2", summary)
	service, tools := seedPlan(t, runID, []agent.PlanStep{first, second})

	// 第一次推进：只有第一步请求审批。
	if _, err := service.Resume(ctx, testSubject(), runID); err == nil {
		t.Fatal("未批准时 Resume 不应成功")
	}
	counts := approvalEvents(t, service, runID)
	if len(counts) != 1 || counts[first.StepID] != 1 {
		t.Fatalf("应只有第一步请求审批，得到 %#v", counts)
	}
	if tools.OutreachCount() != 0 {
		t.Fatalf("批准前不得写入，writes=%d", tools.OutreachCount())
	}

	// 批准第一步：放行第一步，但第二步必须**再次**请求审批。
	if err := service.Approve(ctx, testSubject(), runID, first.StepID, "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(ctx, testSubject(), runID); err == nil {
		t.Fatal("第二步未批准时 Resume 不应成功")
	}
	if tools.OutreachCount() != 1 {
		t.Fatalf("仅第一步应执行，writes=%d", tools.OutreachCount())
	}
	counts = approvalEvents(t, service, runID)
	if len(counts) != 2 || counts[second.StepID] != 1 {
		t.Fatalf("第二步必须各自请求审批（同一 Tool 也不得复用第一步的批准），得到 %#v", counts)
	}
	if toolCallsForStep(t, service, runID, second.StepID) != 0 {
		t.Fatalf("第二步未获批准就产生了 tool_call: %#v", service.Events(runID))
	}

	// 批准第二步（复用第一步的批准必须无效，因此这里必须重新批准）。
	if err := service.Approve(ctx, testSubject(), runID, second.StepID, "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(ctx, testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	if tools.OutreachCount() != 2 {
		t.Fatalf("两次批准应产生两次写入，writes=%d", tools.OutreachCount())
	}
	plan, _ := service.deps.Repository.GetPlan(runID)
	if plan.Status != agent.RunCompleted {
		t.Fatalf("两步批准后应完成，得到 %s", plan.Status)
	}
	if terminalCount(service.Events(runID)) != 1 {
		t.Fatalf("应恰好一个终态事件: %#v", service.Events(runID))
	}
	// 每条副作用 tool_call 之前都必须有绑定到它自己的审批事件。
	approvedBindings := map[string]bool{}
	for _, event := range service.Events(runID) {
		binding := event.Data["step_id"] + "/" + event.Data["worker_id"] + "/" + event.Data["tool_id"]
		switch event.Type {
		case agent.ApprovalRequired:
			approvedBindings[binding] = true
		case agent.ToolCall:
			if event.Data["tool_id"] == examplebusiness.SideEffectToolID && !approvedBindings[binding] {
				t.Fatalf("副作用 tool_call 缺少绑定到它自己的批准: %#v", event.Data)
			}
		}
	}
}

// TestApprovedStepIsNotExecutedAfterCancel 覆盖 spec §4 的 Edge Case「步骤在批准后被取消」。
//
// 已批准但尚未 Resume 的 Run 被取消后，副作用不得执行；批准记录本身保留 approve 事实
// （审计要求），但终态 Run 的 Resume 只能重放事件。
func TestApprovedStepIsNotExecutedAfterCancel(t *testing.T) {
	ctx := context.Background()
	runID := "run-approve-then-cancel"
	step := sideEffectStep(runID, "step-1", registeredSummary(t, examplebusiness.SideEffectToolID))
	service, tools := seedPlan(t, runID, []agent.PlanStep{step})

	if _, err := service.Resume(ctx, testSubject(), runID); err == nil {
		t.Fatal("未批准时 Resume 不应成功")
	}
	if err := service.Approve(ctx, testSubject(), runID, step.StepID, "approve"); err != nil {
		t.Fatal(err)
	}
	record, ok := service.deps.Repository.GetApproval(runID, step.StepID)
	if !ok || record.Status != approval.Approved {
		t.Fatalf("审批应已批准: %#v", record)
	}

	// 批准之后、Resume 之前取消。
	if err := service.Cancel(ctx, testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(ctx, testSubject(), runID); err != nil {
		t.Fatalf("终态 Run 的 Resume 应重放事件而不是报错: %v", err)
	}
	if tools.OutreachCount() != 0 {
		t.Fatalf("已批准但被取消的步骤不得执行，writes=%d", tools.OutreachCount())
	}
	if toolCallsForStep(t, service, runID, step.StepID) != 0 {
		t.Fatalf("取消后不得产生 tool_call: %#v", service.Events(runID))
	}
	plan, _ := service.deps.Repository.GetPlan(runID)
	if plan.Status != agent.RunCanceled {
		t.Fatalf("终态应为 canceled，得到 %s", plan.Status)
	}
	if terminalCount(service.Events(runID)) != 1 {
		t.Fatalf("应恰好一个终态事件: %#v", service.Events(runID))
	}
	// 批准事实本身不被抹掉：审计要能回答"批准过、但未执行"。
	after, ok := service.deps.Repository.GetApproval(runID, step.StepID)
	if !ok || after.Status != approval.Approved || after.Reviewer != testSubject().SubjectID {
		t.Fatalf("批准记录应保留批准事实与决策人: %#v", after)
	}
}
