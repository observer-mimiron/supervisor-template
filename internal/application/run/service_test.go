package run

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/conversation"
	"github.com/observer-mimiron/supervisor-template/internal/domain/identity"
	"github.com/observer-mimiron/supervisor-template/internal/domain/operation"
	authinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/auth"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/checkpoint"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/eventbus"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/llm"
	meminfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/memory"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/persistence"
	toolinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/tool"
)

func newTestService() (*Service, *toolinfra.FakeRegistry) {
	repository := persistence.NewMemoryRepository()
	checkpoints := checkpoint.NewMemoryStore()
	events := eventbus.NewMemoryBus()
	tools := toolinfra.NewFakeRegistry()
	deps := application.Dependencies{
		Repository: repository,
		Checkpoint: checkpoints,
		EventBus:   events,
		Supervisor: llm.NewFakeSupervisor(),
		Policy: agent.NewPolicyGate([]operation.WorkerContract{{
			WorkerID:     "user_analysis",
			AllowedTools: []string{"user_query", "simulated_outreach"},
		}}, toolinfra.Contracts()),
		RunAuth: authinfra.OwnerRunAuthorizer{},
		Tools:   tools,
	}
	return NewService(deps), tools
}

func TestFileBackedServiceRestoresApprovalAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	newFileService := func() (*Service, *toolinfra.FakeRegistry) {
		repository, err := persistence.NewFileRepository(filepath.Join(dir, "runs"))
		if err != nil {
			t.Fatal(err)
		}
		checkpoints, err := checkpoint.NewFileStore(filepath.Join(dir, "checkpoints"))
		if err != nil {
			t.Fatal(err)
		}
		events, err := eventbus.NewFileBus(filepath.Join(dir, "events"))
		if err != nil {
			t.Fatal(err)
		}
		tools := toolinfra.NewFakeRegistry()
		deps := application.Dependencies{
			Repository: repository,
			Checkpoint: checkpoints,
			EventBus:   events,
			Supervisor: llm.NewFakeSupervisor(),
			Policy: agent.NewPolicyGate([]operation.WorkerContract{{
				WorkerID: "user_analysis", AllowedTools: []string{"user_query", "simulated_outreach"},
			}}, toolinfra.Contracts()),
			RunAuth: authinfra.OwnerRunAuthorizer{},
			Tools:   tools,
		}
		return NewService(deps), tools
	}
	first, _ := newFileService()
	runID, err := first.Start(context.Background(), request("run-restart", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	second, tools := newFileService()
	if _, err := second.Resume(context.Background(), testSubject(), runID); err == nil || !strings.Contains(err.Error(), "审批") {
		t.Fatalf("restart lost approval state: %v", err)
	}
	if err := second.Approve(context.Background(), testSubject(), runID, "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Resume(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	events := second.Events(runID)
	eventIDs := make(map[string]struct{}, len(events))
	for _, event := range events {
		if _, exists := eventIDs[event.EventID]; exists {
			t.Fatalf("restart resume reused event id %q: %#v", event.EventID, events)
		}
		eventIDs[event.EventID] = struct{}{}
	}
	if tools.OutreachCount() != 1 || terminalCount(events) != 1 {
		t.Fatalf("restart resume did not complete once: count=%d events=%#v", tools.OutreachCount(), second.Events(runID))
	}
}

func TestReadOnlyRunCompletesOnceAndReplaysTerminalEvents(t *testing.T) {
	service, _ := newTestService()
	runID, err := service.Start(context.Background(), request("run-read", "分析示例用户分群"))
	if err != nil {
		t.Fatal(err)
	}
	events := service.Events(runID)
	if terminalCount(events) != 1 || events[len(events)-1].Type != agent.Completed {
		t.Fatalf("unexpected terminal events: %#v", events)
	}
	originalCount := len(events)
	replayed, err := service.Resume(context.Background(), testSubject(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != originalCount || terminalCount(replayed) != 1 {
		t.Fatalf("resume changed terminal event set: %#v", replayed)
	}
}

func TestReadOnlyRunEmitsCompleteAuditSequence(t *testing.T) {
	service, _ := newTestService()
	runID, err := service.Start(context.Background(), request("run-audit", "分析示例用户分群"))
	if err != nil {
		t.Fatal(err)
	}
	var types []agent.EventType
	for _, event := range service.Events(runID) {
		types = append(types, event.Type)
	}
	want := []agent.EventType{agent.Started, agent.Decision, agent.Plan, agent.Progress, agent.ToolCall, agent.Progress, agent.Text, agent.Completed}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("audit event types = %#v, want %#v", types, want)
	}
}

func TestRunUsesConfiguredWorkerRunner(t *testing.T) {
	service, _ := newTestService()
	runner := &recordingRunner{result: "runner result"}
	service.deps.Runner = runner

	runID, err := service.Start(context.Background(), request("run-runner", "分析示例用户分群"))
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.requests))
	}
	call := runner.requests[0]
	if call.RunID != runID || call.WorkerID != "user_analysis" || call.Intent != "user_analysis" || call.ToolID != "user_query" {
		t.Fatalf("unexpected worker request: %#v", call)
	}
	if call.IdempotencyKey == "" || call.Input["message"] == "" {
		t.Fatalf("worker request lost execution context: %#v", call)
	}
	if call.Deadline.IsZero() || time.Until(call.Deadline) <= 0 {
		t.Fatalf("runner request lost fixed deadline: %#v", call)
	}
	events := service.Events(runID)
	if events[len(events)-1].Type != agent.Completed || events[len(events)-2].Data["content"] != "runner result" {
		t.Fatalf("runner result was not projected: %#v", events)
	}
}

func TestWorkerRunnerReceivesCancellationContext(t *testing.T) {
	service, _ := newTestService()
	runner := &cancelAwareRunner{}
	service.deps.Runner = runner
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runID, err := service.Start(ctx, request("run-canceled-before-call", "分析示例用户分群"))
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorCanceled {
		t.Fatalf("expected cooperative cancellation, got %v", err)
	}
	if runner.calls != 0 || terminalCount(service.Events(runID)) != 1 {
		t.Fatalf("runner was called after cancellation: calls=%d events=%#v", runner.calls, service.Events(runID))
	}
}

func TestEndpointCancelStopsInFlightRunnerAndEmitsOneCanceledEvent(t *testing.T) {
	service, _ := newTestService()
	runner := &blockingRunner{started: make(chan struct{})}
	service.deps.Runner = runner
	result := make(chan error, 1)
	go func() {
		_, err := service.Start(context.Background(), request("run-cancel-in-flight", "分析示例用户分群"))
		result <- err
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	if err := service.Cancel(context.Background(), testSubject(), "run-cancel-in-flight"); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("start should report cancellation")
	}
	events := service.Events("run-cancel-in-flight")
	if runner.canceled == 0 || terminalCount(events) != 1 || events[len(events)-1].Type != agent.Canceled {
		t.Fatalf("in-flight cancellation was not observed: canceled=%d events=%#v", runner.canceled, events)
	}
}

func TestUnknownRunnerOutcomeWaitsForReconciliationAndNeverRetries(t *testing.T) {
	service, _ := newTestService()
	runner := &unknownOutcomeRunner{}
	service.deps.Runner = runner
	runID, err := service.Start(context.Background(), request("run-unknown-outcome", "分析示例用户分群"))
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorOutcomeUnknown {
		t.Fatalf("expected unknown outcome, got %v", err)
	}
	if runner.calls != 1 || !hasEvent(service.Events(runID), agent.ReconciliationRequired) {
		t.Fatalf("unknown outcome was not recorded: calls=%d events=%#v", runner.calls, service.Events(runID))
	}
	if _, err := service.Resume(context.Background(), testSubject(), runID); !errors.As(err, &runErr) || runErr.Code != agent.ErrorOutcomeUnknown {
		t.Fatalf("resume should remain reconciliation-gated, got %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("resume retried unknown external call: calls=%d", runner.calls)
	}
}

func TestKnownPreCallFailureRetriesWithinBudget(t *testing.T) {
	service, _ := newTestService()
	runner := &preCallRunner{}
	service.deps.Runner = runner
	service.deps.Budget = application.ExecutionBudget{MaxPlanSteps: 1, MaxToolCalls: 2, MaxRetries: 1, CostBudget: 2, Timeout: time.Second}
	runID, err := service.Start(context.Background(), request("run-pre-call-retry", "分析示例用户分群"))
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 2 || !hasEvent(service.Events(runID), agent.Completed) {
		t.Fatalf("pre-call failure was not retried once: calls=%d events=%#v", runner.calls, service.Events(runID))
	}
}

func TestExecutionPlanRunsStepsInOrderAndHonorsRunBudget(t *testing.T) {
	steps := []agent.PlanStep{
		{StepID: "step-1", WorkerID: "user_analysis", Intent: "query", ToolID: "user_query", Input: map[string]string{"message": "first"}, Status: agent.StepPending, IdempotencyKey: "run-multi:step-1"},
		{StepID: "step-2", WorkerID: "user_analysis", Intent: "query", ToolID: "user_query", Input: map[string]string{"message": "second"}, Status: agent.StepPending, IdempotencyKey: "run-multi:step-2"},
	}
	service, plan := prepareExecutionPlan(t, "run-multi", steps, application.ExecutionBudget{MaxPlanSteps: 2, MaxToolCalls: 2, MaxRetries: 0, CostBudget: 2, Timeout: time.Second})
	runner := &sequenceRunner{}
	service.deps.Runner = runner
	service.mu.Lock()
	err := service.executeLocked(context.Background(), plan)
	service.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 2 || runner.requests[0].Input["message"] != "first" || runner.requests[1].Input["message"] != "second" {
		t.Fatalf("steps were not executed in order: %#v", runner.requests)
	}
	if terminalCount(service.Events("run-multi")) != 1 {
		t.Fatalf("expected exactly one terminal event: %#v", service.Events("run-multi"))
	}

	limited, limitedPlan := prepareExecutionPlan(t, "run-budget-multi", steps, application.ExecutionBudget{MaxPlanSteps: 2, MaxToolCalls: 1, MaxRetries: 2, CostBudget: 2, Timeout: time.Second})
	limitedRunner := &sequenceRunner{}
	limited.deps.Runner = limitedRunner
	limited.mu.Lock()
	err = limited.executeLocked(context.Background(), limitedPlan)
	limited.mu.Unlock()
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorBudgetExceeded {
		t.Fatalf("expected run-level call budget failure, got %v", err)
	}
	if len(limitedRunner.requests) != 1 || terminalCount(limited.Events("run-budget-multi")) != 1 {
		t.Fatalf("budget was not enforced across steps: calls=%d events=%#v", len(limitedRunner.requests), limited.Events("run-budget-multi"))
	}
}

func TestWorkerReceivesBoundedScopedMemoryWithoutChangingPlanAuthority(t *testing.T) {
	service, _ := newTestService()
	store := meminfra.NewStore()
	service.deps.MemoryStore = store
	service.deps.MemoryRead = store
	subject := testSubject()
	scope := conversation.MemoryScope{TenantID: subject.TenantID, SubjectID: subject.SubjectID, ConversationID: "demo", Kind: conversation.MemoryWorking}
	if _, err := store.Append(context.Background(), scope, conversation.MemoryEntry{MemoryID: "memory-1", Scope: scope, Content: "relevant context"}); err != nil {
		t.Fatal(err)
	}
	foreign := scope
	foreign.SubjectID = "other-user"
	if _, err := store.Append(context.Background(), foreign, conversation.MemoryEntry{MemoryID: "memory-2", Scope: foreign, Content: "foreign context"}); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{result: "done"}
	service.deps.Runner = runner
	runID, err := service.Start(context.Background(), request("run-memory", "分析示例用户分群"))
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 || len(runner.requests[0].Memories) != 1 || runner.requests[0].Memories[0].Content != "relevant context" {
		t.Fatalf("wrong memory scope or recall limit: %#v", runner.requests)
	}
	plan, ok := service.deps.Repository.GetPlan(runID)
	if !ok || plan.Status != agent.RunCompleted || len(plan.Steps) != 1 || plan.Steps[0].ToolID != "user_query" {
		t.Fatalf("memory altered plan authority: %#v", plan)
	}
}

func prepareExecutionPlan(t *testing.T, runID string, steps []agent.PlanStep, budget application.ExecutionBudget) (*Service, agent.ExecutionPlan) {
	t.Helper()
	service, _ := newTestService()
	if err := service.deps.Repository.SaveRequest(request(runID, "test")); err != nil {
		t.Fatal(err)
	}
	plan, err := agent.NewExecutionPlan(runID+":plan", runID, steps, budget.MaxPlanSteps, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.deps.Repository.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	service.deps.Budget = budget
	service.mu.Lock()
	if err := service.emitLocked(runID, agent.Started, map[string]string{"conversation_id": "demo"}); err != nil {
		service.mu.Unlock()
		t.Fatal(err)
	}
	service.mu.Unlock()
	return service, plan
}

func TestPreCallFailureStopsAtCallAndCostBudget(t *testing.T) {
	service, _ := newTestService()
	runner := &alwaysPreCallRunner{}
	service.deps.Runner = runner
	service.deps.Budget = application.ExecutionBudget{MaxPlanSteps: 1, MaxToolCalls: 1, MaxRetries: 5, CostBudget: 1, Timeout: time.Second}
	runID, err := service.Start(context.Background(), request("run-budget", "分析示例用户分群"))
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorBudgetExceeded {
		t.Fatalf("expected budget rejection, got %v", err)
	}
	if runner.calls != 1 || !hasEvent(service.Events(runID), agent.Failed) {
		t.Fatalf("budget allowed extra call: calls=%d events=%#v", runner.calls, service.Events(runID))
	}
}

func TestResumeRepairsMissingTerminalEventOnce(t *testing.T) {
	service, _ := newTestService()
	store := &repairingEventStore{}
	service.deps.EventBus = store
	runID, err := service.Start(context.Background(), request("run-repair-terminal", "分析示例用户分群"))
	if err != nil {
		t.Fatal(err)
	}
	if hasEvent(store.Events(runID), agent.Completed) {
		t.Fatal("test store unexpectedly retained terminal event")
	}
	if _, err := service.Resume(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	if terminalCount(store.Events(runID)) != 1 {
		t.Fatalf("terminal repair was not idempotent: %#v", store.Events(runID))
	}
}

func TestApprovalBlocksSideEffectAndRepeatedResumeIsIdempotent(t *testing.T) {
	service, tools := newTestService()
	runID, err := service.Start(context.Background(), request("run-side-effect", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	if tools.OutreachCount() != 0 || !hasEvent(service.Events(runID), agent.ApprovalRequired) {
		t.Fatalf("side effect escaped approval: %#v", service.Events(runID))
	}
	if _, err := service.Resume(context.Background(), testSubject(), runID); err == nil {
		t.Fatal("expected approval requirement")
	}
	if err := service.Approve(context.Background(), testSubject(), runID, "approve"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := service.Resume(context.Background(), testSubject(), runID); err != nil {
			t.Fatal(err)
		}
	}
	if tools.OutreachCount() != 1 || terminalCount(service.Events(runID)) != 1 {
		t.Fatalf("idempotency failed: count=%d events=%#v", tools.OutreachCount(), service.Events(runID))
	}
}

func TestSideEffectRunEmitsApprovalAuditSequence(t *testing.T) {
	service, _ := newTestService()
	runID, err := service.Start(context.Background(), request("run-approval-audit", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), testSubject(), runID, "approve"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	var types []agent.EventType
	for _, event := range service.Events(runID) {
		types = append(types, event.Type)
	}
	want := []agent.EventType{agent.Started, agent.Decision, agent.Plan, agent.ApprovalRequired, agent.Progress, agent.ToolCall, agent.Progress, agent.Text, agent.Completed}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("approval audit event types = %#v, want %#v", types, want)
	}
}

func TestRejectedApprovalNeverCallsSideEffectTool(t *testing.T) {
	service, tools := newTestService()
	runID, err := service.Start(context.Background(), request("run-reject", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Approve(context.Background(), testSubject(), runID, "reject"); err != nil {
		t.Fatal(err)
	}
	events := service.Events(runID)
	if tools.OutreachCount() != 0 || terminalCount(events) != 1 || events[len(events)-1].Type != agent.Failed {
		t.Fatalf("rejected run is invalid: %#v", events)
	}
}

func TestUnknownCapabilityIsClassifiedAndNeverExecuted(t *testing.T) {
	service, tools := newTestService()
	runID, err := service.Start(context.Background(), request("run-unknown", "未知能力"))
	if err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("expected unknown capability error, got %v", err)
	}
	events := service.Events(runID)
	if tools.OutreachCount() != 0 || terminalCount(events) != 1 || events[len(events)-1].Type != agent.Failed {
		t.Fatalf("unknown capability was not classified: %#v", events)
	}
}

func TestCancelApprovalRunProducesCanceledTerminal(t *testing.T) {
	service, tools := newTestService()
	runID, err := service.Start(context.Background(), request("run-cancel", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Cancel(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	events := service.Events(runID)
	if tools.OutreachCount() != 0 || terminalCount(events) != 1 || events[len(events)-1].Type != agent.Canceled {
		t.Fatalf("cancel did not terminate safely: %#v", events)
	}
	if err := service.Cancel(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	if terminalCount(service.Events(runID)) != 1 {
		t.Fatal("repeated cancel added a terminal event")
	}
}

func TestToolTimeoutIsClassifiedAndDoesNotComplete(t *testing.T) {
	service, _ := newTestService()
	service.deps.Tools = blockingTool{}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	runID, err := service.Start(ctx, request("run-timeout", "分析示例用户分群"))
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorToolTimeout {
		t.Fatalf("expected TOOL_TIMEOUT, got %v", err)
	}
	events := service.Events(runID)
	if events[len(events)-1].Type != agent.Failed || hasEvent(events, agent.Completed) {
		t.Fatalf("timeout terminal is invalid: %#v", events)
	}
}

func TestSensitiveToolResultIsRejectedBeforeTextProjection(t *testing.T) {
	service, _ := newTestService()
	service.deps.Tools = fixedTool{result: "api_key=secret /home/service/.env"}
	runID, err := service.Start(context.Background(), request("run-sensitive", "分析示例用户分群"))
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorInvalidOutput {
		t.Fatalf("expected INVALID_OUTPUT, got %v", err)
	}
	events := service.Events(runID)
	if events[len(events)-1].Type != agent.Failed || hasEvent(events, agent.Text) || hasEvent(events, agent.Completed) {
		t.Fatalf("sensitive output escaped guard: %#v", events)
	}
}

func TestExistingRunCannotBeReplayedAcrossConversations(t *testing.T) {
	service, _ := newTestService()
	if _, err := service.Start(context.Background(), request("run-owned", "分析示例用户分群")); err != nil {
		t.Fatal(err)
	}
	_, err := service.Start(context.Background(), conversation.ExecutionRequest{
		RunID:          "run-owned",
		ConversationID: "other-conversation",
		Subject:        testSubject(),
		Message:        "分析示例用户分群",
	})
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorRunNotResumable {
		t.Fatalf("expected ownership rejection, got %v", err)
	}
}

func TestRunAccessIsBoundToOwningSubject(t *testing.T) {
	service, tools := newTestService()
	runID, err := service.Start(context.Background(), request("run-owner", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	other := identity.Subject{TenantID: "other-tenant", SubjectID: "other-user"}
	replay := request(runID, "模拟触达示例用户")
	replay.Subject = other
	if _, err := service.Start(context.Background(), replay); !hasAccessDenied(err) {
		t.Fatalf("replay error = %v", err)
	}
	if err := service.Approve(context.Background(), other, runID, "approve"); !hasAccessDenied(err) {
		t.Fatalf("approval error = %v", err)
	}
	if _, err := service.Resume(context.Background(), other, runID); !hasAccessDenied(err) {
		t.Fatalf("resume error = %v", err)
	}
	if err := service.Cancel(context.Background(), other, runID); !hasAccessDenied(err) {
		t.Fatalf("cancel error = %v", err)
	}
	if tools.OutreachCount() != 0 || !hasEvent(service.Events(runID), agent.ApprovalRequired) {
		t.Fatalf("foreign subject changed run state: %#v", service.Events(runID))
	}
}

type fixedTool struct{ result string }

type recordingRunner struct {
	requests []application.WorkerRequest
	result   string
}

type sequenceRunner struct{ requests []application.WorkerRequest }

func (r *sequenceRunner) Run(_ context.Context, request application.WorkerRequest) (application.WorkerResult, error) {
	r.requests = append(r.requests, request)
	return application.WorkerResult{Content: request.Input["message"] + " result"}, nil
}

type cancelAwareRunner struct{ calls int }

type preCallRunner struct{ calls int }

func (r *preCallRunner) Run(context.Context, application.WorkerRequest) (application.WorkerResult, error) {
	r.calls++
	if r.calls == 1 {
		return application.WorkerResult{}, &application.PreCallError{Err: errors.New("not started")}
	}
	return application.WorkerResult{Content: "retry succeeded"}, nil
}

type alwaysPreCallRunner struct{ calls int }

func (r *alwaysPreCallRunner) Run(context.Context, application.WorkerRequest) (application.WorkerResult, error) {
	r.calls++
	return application.WorkerResult{}, &application.PreCallError{Err: errors.New("not started")}
}

type blockingRunner struct {
	started  chan struct{}
	canceled int
}

func (r *blockingRunner) Run(ctx context.Context, _ application.WorkerRequest) (application.WorkerResult, error) {
	close(r.started)
	<-ctx.Done()
	r.canceled++
	return application.WorkerResult{}, ctx.Err()
}

func (r *cancelAwareRunner) Run(ctx context.Context, _ application.WorkerRequest) (application.WorkerResult, error) {
	r.calls++
	return application.WorkerResult{}, ctx.Err()
}

type unknownOutcomeRunner struct{ calls int }

func (r *unknownOutcomeRunner) Run(context.Context, application.WorkerRequest) (application.WorkerResult, error) {
	r.calls++
	return application.WorkerResult{}, &application.OutcomeUnknownError{Err: errors.New("commit uncertain")}
}

type repairingEventStore struct {
	events          map[string][]agent.RunEvent
	droppedTerminal bool
}

func (s *repairingEventStore) Append(event agent.RunEvent) (agent.RunEvent, error) {
	if s.events == nil {
		s.events = make(map[string][]agent.RunEvent)
	}
	if event.Type == agent.Completed && !s.droppedTerminal {
		s.droppedTerminal = true
		return event, nil
	}
	items := s.events[event.RunID]
	event.Sequence = int64(len(items) + 1)
	s.events[event.RunID] = append(items, event)
	return event, nil
}

func (s *repairingEventStore) Events(runID string) []agent.RunEvent {
	return append([]agent.RunEvent(nil), s.events[runID]...)
}

func (r *recordingRunner) Run(_ context.Context, request application.WorkerRequest) (application.WorkerResult, error) {
	r.requests = append(r.requests, request)
	return application.WorkerResult{Content: r.result}, nil
}

func (t fixedTool) Execute(context.Context, string, map[string]string, string) (string, error) {
	return t.result, nil
}

type blockingTool struct{}

func (blockingTool) Execute(ctx context.Context, _ string, _ map[string]string, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func request(runID, message string) conversation.ExecutionRequest {
	return conversation.ExecutionRequest{RunID: runID, ConversationID: "demo", Subject: testSubject(), Message: message}
}

func testSubject() identity.Subject {
	return identity.Subject{TenantID: "test-tenant", SubjectID: "test-user"}
}

func hasAccessDenied(err error) bool {
	var runErr *Error
	return errors.As(err, &runErr) && runErr.Code == agent.ErrorAccessDenied
}

func terminalCount(events []agent.RunEvent) int {
	count := 0
	for _, event := range events {
		if agent.IsTerminal(eventbus.TerminalStatus(event.Type)) {
			count++
		}
	}
	return count
}

func hasEvent(events []agent.RunEvent, want agent.EventType) bool {
	for _, event := range events {
		if event.Type == want {
			return true
		}
	}
	return false
}
