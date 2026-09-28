package run

import (
	"context"
	"testing"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

func TestAdaptStepRunnerPassesApprovedStepAndResumeToken(t *testing.T) {
	wantStep := agent.PlanStep{StepID: "step-1", WorkerID: "worker", ToolID: "tool", Status: agent.StepRunning}
	fake := &recordingStepRunner{}
	got, err := (AdaptStepRunner{Runner: fake}).Run(context.Background(), application.WorkerRequest{
		RunID: "run-1", Step: wantStep, WorkerID: "worker", ToolID: "tool", ResumeToken: "checkpoint-1",
		Deadline: time.Now().Add(time.Minute), ToolBudget: 2, ModelBudget: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fake.resumedToken != "checkpoint-1" || fake.step.StepID != wantStep.StepID || fake.step.ToolID != wantStep.ToolID || got.Content != "resumed" || got.Checkpoint != "checkpoint-2" {
		t.Fatalf("resume contract was not preserved: fake=%#v result=%#v", fake, got)
	}
}

func TestAdaptStepRunnerRejectsUnapprovedStep(t *testing.T) {
	fake := &recordingStepRunner{}
	_, err := (AdaptStepRunner{Runner: fake}).Run(context.Background(), application.WorkerRequest{
		RunID: "run-1", WorkerID: "worker", ToolID: "tool",
		Step: agent.PlanStep{StepID: "step-1", WorkerID: "worker", ToolID: "other", Status: agent.StepRunning},
	})
	if err == nil || fake.runs != 0 || fake.resumedToken != "" {
		t.Fatalf("invalid step reached runner: err=%v fake=%#v", err, fake)
	}
}

type recordingStepRunner struct {
	step         agent.PlanStep
	resumedToken string
	runs         int
}

func (r *recordingStepRunner) RunStep(_ context.Context, _ RunnerSession, step agent.PlanStep) (StepResult, error) {
	r.runs++
	r.step = step
	return StepResult{Content: "ran"}, nil
}

func (r *recordingStepRunner) ResumeStep(_ context.Context, _ RunnerSession, token string, step agent.PlanStep) (StepResult, error) {
	r.resumedToken = token
	r.step = step
	return StepResult{Content: "resumed", Checkpoint: "checkpoint-2"}, nil
}
