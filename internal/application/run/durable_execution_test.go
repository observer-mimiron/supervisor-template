package run

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/persistence"
)

func TestServiceLeaseRejectsCompetingStart(t *testing.T) {
	first, _ := newTestService()
	leaseStore := persistence.NewMemoryRunLeaseStore()
	first.deps.Leases = leaseStore
	first.deps.Runner = &blockingRunner{started: make(chan struct{})}
	second := NewService(first.deps)

	result := make(chan error, 1)
	go func() {
		_, err := first.Start(context.Background(), request("run-lease-busy", "分析示例用户分群"))
		result <- err
	}()
	select {
	case <-first.deps.Runner.(*blockingRunner).started:
	case <-time.After(time.Second):
		t.Fatal("first owner did not reach the runner")
	}

	_, err := second.Start(context.Background(), request("run-lease-busy", "分析示例用户分群"))
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorRunBusy {
		t.Fatalf("competing owner error = %v, want RUN_BUSY", err)
	}
	if err := first.Cancel(context.Background(), testSubject(), "run-lease-busy"); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("first owner should observe cancellation")
	}
}

func TestConcurrentApproveResumeCancelUsesSingleLease(t *testing.T) {
	service, tools := newTestService()
	leaseStore := persistence.NewMemoryRunLeaseStore()
	service.deps.Leases = leaseStore
	runID, err := service.Start(context.Background(), request("run-control-race", "模拟触达示例用户"))
	if err != nil {
		t.Fatal(err)
	}
	second := NewService(service.deps)
	third := NewService(service.deps)
	errs := make(chan error, 3)
	done := make(chan struct{}, 3)
	go func() {
		errs <- service.Approve(context.Background(), testSubject(), runID, "approve")
		done <- struct{}{}
	}()
	go func() {
		_, err := second.Resume(context.Background(), testSubject(), runID)
		errs <- err
		done <- struct{}{}
	}()
	go func() {
		errs <- third.Cancel(context.Background(), testSubject(), runID)
		done <- struct{}{}
	}()
	for range 3 {
		<-done
	}
	close(errs)
	for err := range errs {
		_ = err
	}
	events := service.Events(runID)
	if terminalCount(events) != 1 {
		t.Fatalf("control race produced %d terminal events: %#v", terminalCount(events), events)
	}
	if tools.OutreachCount() > 1 {
		t.Fatalf("control race duplicated side effect: %d", tools.OutreachCount())
	}
}

func TestResumeTakesOverExpiredRunningStepWithoutRetry(t *testing.T) {
	service, _ := newTestService()
	leaseStore := persistence.NewMemoryRunLeaseStore()
	service.deps.Leases = leaseStore
	clock := newDurableTestClock(time.Unix(1_700_000_000, 0))
	service.now = clock.Now
	requestID := "run-expired-recovery"
	if err := service.deps.Repository.SaveRequest(request(requestID, "分析示例用户分群")); err != nil {
		t.Fatal(err)
	}
	plan, err := agent.NewExecutionPlan(requestID+":plan", requestID, []agent.PlanStep{{
		StepID: "step-1", WorkerID: "user_analysis", Intent: "query", ToolID: "user_query",
		Input: map[string]string{"message": "query"}, Status: agent.StepPending, IdempotencyKey: requestID + ":step-1",
	}}, 1, clock.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.TransitionStep("step-1", agent.StepRunning); err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Attempts = 1
	plan.Steps[0].AttemptStatus = "running"
	if err := service.deps.Repository.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := service.deps.Checkpoint.Save(agent.Checkpoint{RunID: requestID, PlanID: plan.PlanID, Status: agent.RunRunning, Version: 1, NextStepID: "step-1"}); err != nil {
		t.Fatal(err)
	}
	old := newTestLease(t, requestID, clock.Now(), time.Second)
	claimed, err := leaseStore.Claim(context.Background(), old, clock.Now())
	if err != nil || !claimed {
		t.Fatalf("seed lease claim = %v, %v", claimed, err)
	}
	clock.Advance(2 * time.Second)
	runner := &recordingRunner{result: "must not retry"}
	service.deps.Runner = runner

	_, err = service.Resume(context.Background(), testSubject(), requestID)
	var runErr *Error
	if !errors.As(err, &runErr) || runErr.Code != agent.ErrorOutcomeUnknown {
		t.Fatalf("expired running step error = %v, want RUN_OUTCOME_UNKNOWN", err)
	}
	stored, ok := service.deps.Repository.GetPlan(requestID)
	if !ok || stored.Status != agent.RunWaitingReconciliation || stored.Steps[0].Status != agent.StepWaitingReconciliation {
		t.Fatalf("recovery state = %#v, want waiting_reconciliation", stored)
	}
	if stored.ErrorClass != agent.ClassUnknownOutcome || stored.Steps[0].ErrorClass != agent.ClassUnknownOutcome {
		t.Fatalf("recovery error class = plan=%q step=%q", stored.ErrorClass, stored.Steps[0].ErrorClass)
	}
	if len(runner.requests) != 0 || len(service.Events(requestID)) != 1 || service.Events(requestID)[0].Type != agent.ReconciliationRequired {
		t.Fatalf("expired takeover retried or projected incorrectly: calls=%d events=%#v", len(runner.requests), service.Events(requestID))
	}
}

func TestTerminalEventAppendFailureIsRecoverableAndIdempotent(t *testing.T) {
	service, _ := newTestService()
	base := service.deps.EventBus
	store := &failCompletedOnceStore{base: base}
	service.deps.EventBus = store
	runID, err := service.Start(context.Background(), request("run-terminal-event-failure", "分析示例用户分群"))
	if err == nil {
		t.Fatal("expected terminal event append failure")
	}
	plan, ok := service.deps.Repository.GetPlan(runID)
	if !ok || plan.Status != agent.RunCompleted {
		t.Fatalf("plan was not durably terminal before projection failure: %#v", plan)
	}
	if _, err := service.Resume(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resume(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	events := service.Events(runID)
	if len(events) == 0 || terminalCount(events) != 1 || events[len(events)-1].Type != agent.Completed {
		t.Fatalf("terminal projection was not repaired once: %#v", events)
	}
}

func TestResumeRecoversPreparedStepBeforeRunnerCall(t *testing.T) {
	service, _ := newTestService()
	leaseStore := persistence.NewMemoryRunLeaseStore()
	service.deps.Leases = leaseStore
	clock := time.Now()
	service.now = func() time.Time { return clock }
	runID := "run-prepared-recovery"
	if err := service.deps.Repository.SaveRequest(request(runID, "分析示例用户分群")); err != nil {
		t.Fatal(err)
	}
	plan, err := agent.NewExecutionPlan(runID+":plan", runID, []agent.PlanStep{{
		StepID: "step-1", WorkerID: "user_analysis", Intent: "query", ToolID: "user_query",
		Input: map[string]string{"message": "query"}, Status: agent.StepPending, IdempotencyKey: runID + ":step-1",
	}}, 1, clock.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.TransitionStep("step-1", agent.StepRunning); err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Attempts = 1
	plan.Steps[0].AttemptStatus = "prepared"
	if err := service.deps.Repository.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := service.deps.Checkpoint.Save(agent.Checkpoint{RunID: runID, PlanID: plan.PlanID, Status: agent.RunRunning, Version: 1, NextStepID: "step-1"}); err != nil {
		t.Fatal(err)
	}
	old := application.RunLease{RunID: runID, OwnerToken: "crashed-owner", ExpiresAt: clock.Add(time.Second)}
	if claimed, err := leaseStore.Claim(context.Background(), old, clock); err != nil || !claimed {
		t.Fatalf("seed lease claim = %v, %v", claimed, err)
	}
	clock = clock.Add(2 * time.Second)
	runner := &recordingRunner{result: "recovered"}
	service.deps.Runner = runner
	if _, err := service.Resume(context.Background(), testSubject(), runID); err != nil {
		t.Fatal(err)
	}
	stored, ok := service.deps.Repository.GetPlan(runID)
	if !ok || stored.Status != agent.RunCompleted || stored.Steps[0].Attempts != 1 || stored.Steps[0].AttemptStatus != "succeeded" {
		t.Fatalf("prepared recovery state = %#v", stored)
	}
	if len(runner.requests) != 1 || terminalCount(service.Events(runID)) != 1 {
		t.Fatalf("prepared recovery did not run exactly once: calls=%d events=%#v", len(runner.requests), service.Events(runID))
	}
}

type failCompletedOnceStore struct {
	base application.EventStore
	fail bool
}

func (s *failCompletedOnceStore) Append(event agent.RunEvent) (agent.RunEvent, error) {
	if event.Type == agent.Completed && !s.fail {
		s.fail = true
		return agent.RunEvent{}, errors.New("injected terminal event failure")
	}
	return s.base.Append(event)
}

func (s *failCompletedOnceStore) Events(runID string) []agent.RunEvent { return s.base.Events(runID) }
