// Package eino adapts Eino ADK to the framework-neutral application Runner.
package eino

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/application/run"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
)

// Runner executes one already-approved step. Eino never advances a domain plan.
type Runner struct {
	runner    adkRunner
	store     compose.CheckPointStore
	factory   *AgentFactory
	tools     application.ToolExecutor
	validator application.ToolContractValidator
	contracts map[string]domaintool.Contract
}

// AgentFactory creates one short-lived ADK agent for one approved step.
// Config and approved dependencies are converted into one executable
// node/agent, so a Worker swap never touches the run state machine.
type AgentFactory struct {
	model       model.ToolCallingChatModel
	instruction string
}

// defaultWorkerInstruction 是 Worker 未声明 prompt_file 时的兜底指令。
// 配置了 prompt_file 的 Worker 会用它自己的提示词覆盖该默认值。
const defaultWorkerInstruction = "你是已通过策略审批的 Worker。只能调用提供的工具一次，并且必须使用用户输入中的固定参数。调用完成后直接返回工具结果。"

// NewAgentFactory 创建 Worker 级 Agent 工厂；instruction 为空时使用默认指令。
func NewAgentFactory(chatModel model.ToolCallingChatModel, instruction string) *AgentFactory {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		instruction = defaultWorkerInstruction
	}
	return &AgentFactory{model: chatModel, instruction: instruction}
}

func (f *AgentFactory) Build(ctx context.Context, request application.WorkerRequest, approved *ApprovedTool) (adk.Agent, error) {
	if f == nil || f.model == nil || approved == nil {
		return nil, errors.New("Eino AgentFactory 依赖未装配")
	}
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        request.WorkerID,
		Description: request.Intent,
		Instruction: f.instruction,
		Model:       f.model,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{approved}},
			ReturnDirectly:  map[string]bool{request.ToolID: true},
		},
	})
}

type adkRunner interface {
	Query(context.Context, string, ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent]
	Resume(context.Context, string, ...adk.AgentRunOption) (*adk.AsyncIterator[*adk.AgentEvent], error)
}

func NewRunner(ctx context.Context, agent adk.Agent, store compose.CheckPointStore) *Runner {
	return &Runner{runner: adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true, CheckPointStore: store}), store: store}
}

// NewAgentRunner builds an ADK runner whose ToolNode invokes the shared Tool route.
// instruction 是该 Worker 的模型指令（来自配置声明的 prompt_file）。
func NewAgentRunner(ctx context.Context, chatModel model.ToolCallingChatModel, store compose.CheckPointStore, contracts []domaintool.Contract, executor application.ToolExecutor, validator application.ToolContractValidator, instruction string) (*Runner, error) {
	if chatModel == nil || store == nil || executor == nil || validator == nil {
		return nil, errors.New("Eino Agent Runner 依赖未装配")
	}
	byID := make(map[string]domaintool.Contract, len(contracts))
	for _, contract := range contracts {
		byID[contract.ToolID] = contract
	}
	runner := &Runner{store: store, factory: NewAgentFactory(chatModel, instruction), tools: executor, validator: validator, contracts: byID}
	return runner, nil
}

// WithToolExecutor binds the shared application Tool route for Eino ToolCalls.
func (r *Runner) WithToolExecutor(executor application.ToolExecutor, validator application.ToolContractValidator) *Runner {
	r.tools, r.validator = executor, validator
	return r
}

func (r *Runner) Query(ctx context.Context, input, checkpointID string) *adk.AsyncIterator[*adk.AgentEvent] {
	return r.runner.Query(ctx, input, adk.WithCheckPointID(checkpointID))
}

func (r *Runner) Resume(ctx context.Context, checkpointID string) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	return r.runner.Resume(ctx, checkpointID)
}

func (r *Runner) Run(ctx context.Context, request application.WorkerRequest) (application.WorkerResult, error) {
	if r == nil || ctx == nil || (r.runner == nil && r.factory == nil) {
		return application.WorkerResult{}, &application.PreCallError{Err: errors.New("Eino Runner 或 context 未装配")}
	}
	step := request.Step
	if step.StepID == "" {
		step = agent.PlanStep{StepID: "current", WorkerID: request.WorkerID, ToolID: request.ToolID, Intent: request.Intent, Input: request.Input, Status: agent.StepRunning}
	}
	if request.RunID == "" || step.StepID == "" || step.Status != agent.StepRunning || step.ToolID == "" || step.WorkerID != request.WorkerID || step.ToolID != request.ToolID {
		return application.WorkerResult{}, &application.PreCallError{Err: errors.New("Eino Runner 只接受当前已批准的 running 步骤")}
	}
	query := fmt.Sprintf("worker=%s\nintent=%s\napproved_tool=%s\ninput=%v\n", step.WorkerID, step.Intent, step.ToolID, step.Input)
	checkpointID := request.Session.Checkpoint
	if checkpointID == "" {
		checkpointID = request.RunID + ":" + step.StepID
	}
	activeRunner := r.runner
	if r.factory != nil {
		contract := r.contracts[step.ToolID]
		if contract.ToolID == "" {
			contract = domaintool.Contract{ToolID: step.ToolID, Risk: string(agent.RiskReadOnly)}
		}
		approved, err := NewApprovedTool(contract, step.ToolID, step.Input, step.IdempotencyKey, r.tools, r.validator)
		if err != nil {
			return application.WorkerResult{}, err
		}
		workerAgent, err := r.factory.Build(ctx, request, approved)
		if err != nil {
			return application.WorkerResult{}, &application.PreCallError{Err: err}
		}
		activeRunner = adk.NewRunner(ctx, adk.RunnerConfig{Agent: workerAgent, EnableStreaming: true, CheckPointStore: r.store})
	}
	var iter *adk.AsyncIterator[*adk.AgentEvent]
	if request.ResumeToken != "" {
		checkpointID = request.ResumeToken
		var err error
		iter, err = activeRunner.Resume(ctx, checkpointID)
		if err != nil {
			return application.WorkerResult{}, err
		}
	} else {
		iter = activeRunner.Query(ctx, query, adk.WithCheckPointID(checkpointID))
	}
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
		if r.factory == nil && len(message.ToolCalls) > 0 {
			if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != step.ToolID {
				return application.WorkerResult{}, fmt.Errorf("Eino 请求了未批准的 Tool")
			}
			if r.tools == nil || r.validator == nil {
				return application.WorkerResult{}, &application.PreCallError{Err: errors.New("Eino Tool route 未装配")}
			}
			approved, err := NewApprovedTool(r.contractFor(step.ToolID), step.ToolID, step.Input, step.IdempotencyKey, r.tools, r.validator)
			if err != nil {
				return application.WorkerResult{}, err
			}
			result, err := approved.InvokableRun(ctx, message.ToolCalls[0].Function.Arguments)
			if err != nil {
				return application.WorkerResult{}, err
			}
			return application.WorkerResult{Content: result, Checkpoint: checkpointID}, nil
		}
		if message.Content != "" {
			content += message.Content
		}
	}
	if err := ctx.Err(); err != nil {
		return application.WorkerResult{}, err
	}
	return application.WorkerResult{Content: content, Checkpoint: checkpointID}, nil
}

func (r *Runner) contractFor(toolID string) domaintool.Contract {
	if contract, ok := r.contracts[toolID]; ok {
		return contract
	}
	return domaintool.Contract{ToolID: toolID, Risk: string(agent.RiskReadOnly)}
}

var _ application.WorkerRunner = (*Runner)(nil)
var _ application.WorkerRunner = run.AdaptStepRunner{}
