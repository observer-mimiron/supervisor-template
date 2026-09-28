package run

import (
	"context"
	"errors"
	"fmt"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

// RunnerSession remains an alias for callers that used the run package contract.
type RunnerSession = application.RunnerSession

// StepRunner runs exactly the supplied, already-approved plan step.
type StepRunner interface {
	RunStep(context.Context, RunnerSession, agent.PlanStep) (StepResult, error)
	ResumeStep(context.Context, RunnerSession, string, agent.PlanStep) (StepResult, error)
}

// StepResult is output for the application to validate and persist.
type StepResult struct {
	Content    string
	Checkpoint string
}

// AdaptStepRunner preserves WorkerRunner's existing application wiring.
type AdaptStepRunner struct{ Runner StepRunner }

func (a AdaptStepRunner) Run(ctx context.Context, req application.WorkerRequest) (application.WorkerResult, error) {
	if a.Runner == nil {
		return application.WorkerResult{}, errors.New("step runner 未装配")
	}
	if err := validateStepRequest(req); err != nil {
		return application.WorkerResult{}, err
	}
	session := req.Session
	if session.RunID == "" {
		session.RunID = req.RunID
	}
	if session.Deadline.IsZero() {
		session.Deadline = req.Deadline
	}
	if session.ToolBudget == 0 {
		session.ToolBudget = req.ToolBudget
	}
	if session.ModelBudget == 0 {
		session.ModelBudget = req.ModelBudget
	}
	step := req.Step
	if step.StepID == "" {
		step = agent.PlanStep{StepID: "current", WorkerID: req.WorkerID, Intent: req.Intent, ToolID: req.ToolID, Input: req.Input, Status: agent.StepRunning, IdempotencyKey: req.IdempotencyKey}
	}
	var result StepResult
	var err error
	if req.ResumeToken != "" {
		result, err = a.Runner.ResumeStep(ctx, session, req.ResumeToken, step)
	} else {
		result, err = a.Runner.RunStep(ctx, session, step)
	}
	return application.WorkerResult{Content: result.Content, Checkpoint: result.Checkpoint}, err
}

func validateStepRequest(req application.WorkerRequest) error {
	if req.RunID == "" || req.ToolID == "" {
		return errors.New("runner step 缺少 run_id 或 tool_id")
	}
	if req.Step.StepID != "" && (req.Step.Status != agent.StepRunning || req.Step.ToolID != req.ToolID || req.Step.WorkerID != req.WorkerID) {
		return fmt.Errorf("runner 只能执行当前 running 且已批准的步骤")
	}
	if req.ToolBudget < 0 || req.ModelBudget < 0 {
		return errors.New("runner 预算不能为负数")
	}
	return nil
}
