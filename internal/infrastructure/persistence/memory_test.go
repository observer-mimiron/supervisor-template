package persistence

import (
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

func TestMemoryRepositoryProtectsCompletedSteps(t *testing.T) {
	repo := NewMemoryRepository()
	plan, err := agent.NewExecutionPlan("plan-1", "run-1", []agent.PlanStep{{StepID: "step-1", ToolID: "user_query", Status: agent.StepPending}}, 1, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	if err := plan.TransitionStep("step-1", agent.StepRunning); err != nil {
		t.Fatal(err)
	}
	if err := plan.TransitionStep("step-1", agent.StepSucceeded); err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	plan.Steps[0].Status = agent.StepPending
	if err := repo.SavePlan(plan); err == nil {
		t.Fatal("expected completed step protection")
	}
}
