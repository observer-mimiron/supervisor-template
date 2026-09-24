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

func TestMemoryRepositoryReturnsCloneIsolation(t *testing.T) {
	repo := NewMemoryRepository()
	plan, err := agent.NewExecutionPlan("plan-clone", "run-clone", []agent.PlanStep{{StepID: "step-1", ToolID: "user_query", Status: agent.StepPending, Input: map[string]string{"message": "hello"}}}, 1, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	loaded, ok := repo.GetPlan(plan.RunID)
	if !ok {
		t.Fatal("plan not found")
	}
	loaded.Steps[0].Input["message"] = "tampered"
	loaded.Steps[0].Status = agent.StepRunning
	again, _ := repo.GetPlan(plan.RunID)
	if again.Steps[0].Input["message"] != "hello" || again.Steps[0].Status != agent.StepPending {
		t.Fatalf("repository leaked mutable state: %#v", again)
	}
}
