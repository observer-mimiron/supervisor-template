package agent

import (
	"testing"
	"time"
)

func TestExecutionPlanRejectsInvalidTransitionAndProtectsTerminal(t *testing.T) {
	plan, err := NewExecutionPlan("plan-1", "run-1", []PlanStep{{StepID: "step-1", ToolID: "user_query", Status: StepPending}}, 1, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.TransitionStep("step-1", StepSucceeded); err == nil {
		t.Fatal("expected invalid transition")
	}
	if err := plan.TransitionStep("step-1", StepRunning); err != nil {
		t.Fatal(err)
	}
	if err := plan.TransitionStep("step-1", StepSucceeded); err != nil {
		t.Fatal(err)
	}
	if plan.Status != RunCompleted {
		t.Fatalf("status = %s, want completed", plan.Status)
	}
	if err := plan.MarkTerminal(RunFailed, ""); err == nil {
		t.Fatal("expected terminal overwrite rejection")
	}
}
