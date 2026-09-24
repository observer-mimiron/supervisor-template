// Package eino adapts Eino ADK to the framework-neutral application Runner.
package eino

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/application/run"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
)

// Runner executes one already-approved step. Eino never advances a domain plan.
type Runner struct {
	runner *adk.Runner
}

func NewRunner(ctx context.Context, agent adk.Agent, store compose.CheckPointStore) *Runner {
	return &Runner{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true, CheckPointStore: store})}
}

func (r *Runner) Query(ctx context.Context, input, checkpointID string) *adk.AsyncIterator[*adk.AgentEvent] {
	return r.runner.Query(ctx, input, adk.WithCheckPointID(checkpointID))
}

func (r *Runner) Resume(ctx context.Context, checkpointID string) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	return r.runner.Resume(ctx, checkpointID)
}

func (r *Runner) Run(ctx context.Context, request application.WorkerRequest) (application.WorkerResult, error) {
	if r == nil || r.runner == nil || ctx == nil {
		return application.WorkerResult{}, &application.PreCallError{Err: errors.New("Eino Runner 或 context 未装配")}
	}
	step := request.Step
	if step.StepID == "" {
		step = agent.PlanStep{StepID: "current", WorkerID: request.WorkerID, ToolID: request.ToolID, Intent: request.Intent, Input: request.Input, Status: agent.StepRunning}
	}
	if request.RunID == "" || step.StepID == "" || step.Status != agent.StepRunning || step.ToolID == "" || step.WorkerID != request.WorkerID || step.ToolID != request.ToolID {
		return application.WorkerResult{}, &application.PreCallError{Err: errors.New("Eino Runner 只接受当前已批准的 running 步骤")}
	}
	if request.ResumeToken != "" {
		return application.WorkerResult{}, &application.PreCallError{Err: errors.New("Eino ADK checkpoint resume requires an interrupted checkpoint")}
	}
	query := fmt.Sprintf("worker=%s\nintent=%s\napproved_tool=%s\ninput=%v\n", step.WorkerID, step.Intent, step.ToolID, step.Input)
	checkpointID := request.Session.Checkpoint
	if checkpointID == "" {
		checkpointID = request.RunID + ":" + step.StepID
	}
	iter := r.runner.Query(ctx, query, adk.WithCheckPointID(checkpointID))
	var content string
	for {
		if err := ctx.Err(); err != nil {
			return application.WorkerResult{}, err
		}
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Err != nil {
			return application.WorkerResult{}, event.Err
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return application.WorkerResult{}, err
		}
		if message == nil {
			continue
		}
		if len(message.ToolCalls) > 0 {
			if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != step.ToolID {
				return application.WorkerResult{}, fmt.Errorf("Eino 请求了未批准的 Tool")
			}
			return application.WorkerResult{}, fmt.Errorf("Eino agent 必须将 Tool 调用交由已绑定的 Tool Pool 执行")
		}
		if message.Role == schema.Assistant && message.Content != "" {
			content += message.Content
		}
	}
	if err := ctx.Err(); err != nil {
		return application.WorkerResult{}, err
	}
	return application.WorkerResult{Content: content, Checkpoint: checkpointID}, nil
}

var _ application.WorkerRunner = (*Runner)(nil)
var _ run.StepRunner = run.AdaptStepRunner{}
