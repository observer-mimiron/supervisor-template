package eino

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/application/run"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
	toolinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/tool"
)

func TestMemoryCheckpointStoreCopiesBytes(t *testing.T) {
	store := NewMemoryCheckpointStore().(*MemoryCheckpointStore)
	value := []byte("checkpoint")
	if err := store.Set(context.Background(), "run-1", value); err != nil {
		t.Fatal(err)
	}
	value[0] = 'X'
	got, ok, err := store.Get(context.Background(), "run-1")
	if err != nil || !ok || string(got) != "checkpoint" {
		t.Fatalf("got=%q ok=%v err=%v", got, ok, err)
	}
}

func TestRunnerUsesEinoResumeForResumeToken(t *testing.T) {
	fake := &fakeADKRunner{}
	runner := &Runner{runner: fake}
	request := application.WorkerRequest{
		RunID:       "run-1",
		ResumeToken: "run-1:step-1",
		WorkerID:    "worker",
		ToolID:      "tool",
		Step:        agent.PlanStep{StepID: "step-1", WorkerID: "worker", ToolID: "tool", Status: agent.StepRunning},
	}
	if _, err := runner.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if fake.resumeID != request.ResumeToken || fake.queries != 0 {
		t.Fatalf("resume token was not used: resume=%q queries=%d", fake.resumeID, fake.queries)
	}
}

func TestApprovedToolUsesSharedExecutorAndRejectsParameterMismatch(t *testing.T) {
	executor := &captureToolExecutor{}
	validator := captureToolValidator{}
	approved, err := NewApprovedTool(domaintool.Contract{ToolID: "user_query", Risk: "read_only", RequiredInputs: []string{"message"}}, "user_query", map[string]string{"message": "hello"}, "run-1:step-1", executor, validator)
	if err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"message": "hello"})
	if got, err := approved.InvokableRun(context.Background(), string(args)); err != nil || got != "shared-result" {
		t.Fatalf("approved invocation = %q, %v", got, err)
	}
	if executor.calls != 1 || executor.toolID != "user_query" || executor.key != "run-1:step-1" {
		t.Fatalf("shared executor call = %#v", executor)
	}
	bad, _ := json.Marshal(map[string]string{"message": "changed"})
	if _, err := approved.InvokableRun(context.Background(), string(bad)); err == nil {
		t.Fatal("parameter mismatch was executed")
	}
	if executor.calls != 1 {
		t.Fatal("parameter mismatch reached shared executor")
	}
}

func TestAgentRunnerRoutesToolCallingModelThroughApprovedTool(t *testing.T) {
	executor := &captureToolExecutor{}
	model := &deterministicToolCallingModel{}
	runner, err := NewAgentRunner(context.Background(), model, NewMemoryCheckpointStore(), []domaintool.Contract{{ToolID: "user_query", Risk: "read_only", RequiredInputs: []string{"message"}}}, executor, captureToolValidator{})
	if err != nil {
		t.Fatal(err)
	}
	request := application.WorkerRequest{
		RunID: "run-eino", WorkerID: "worker", Intent: "query", ToolID: "user_query",
		Input: map[string]string{"message": "hello"}, IdempotencyKey: "run-eino:step-1",
		Step: agent.PlanStep{StepID: "step-1", WorkerID: "worker", Intent: "query", ToolID: "user_query", Input: map[string]string{"message": "hello"}, Status: agent.StepRunning, IdempotencyKey: "run-eino:step-1"},
	}
	result, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if executor.calls != 1 || executor.toolID != "user_query" || result.Content == "" {
		t.Fatalf("eino route did not reach shared tool: calls=%d tool=%q result=%q", executor.calls, executor.toolID, result.Content)
	}
}

func TestEinoSideEffectUsesApprovalBoundaryAndIdempotency(t *testing.T) {
	registry, err := toolinfra.NewRegistry("fake.user_query", "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	model := &deterministicToolCallingModel{toolName: "simulated_outreach", arguments: `{"message":"{\"count\":4,\"customer_ids\":[\"cust-001\",\"cust-002\",\"cust-006\",\"cust-008\"],\"spend_365d_total\":6200}"}`}
	runner, err := NewAgentRunner(context.Background(), model, NewMemoryCheckpointStore(), toolinfra.Contracts(), registry, registry)
	if err != nil {
		t.Fatal(err)
	}
	request := application.WorkerRequest{
		RunID: "run-eino-side-effect", WorkerID: "worker", Intent: "outreach", ToolID: "simulated_outreach",
		Input: map[string]string{"message": `{"count":4,"customer_ids":["cust-001","cust-002","cust-006","cust-008"],"spend_365d_total":6200}`}, IdempotencyKey: "run-eino-side-effect:step-1",
		Step: agent.PlanStep{StepID: "step-1", WorkerID: "worker", Intent: "outreach", ToolID: "simulated_outreach", Input: map[string]string{"message": `{"count":4,"customer_ids":["cust-001","cust-002","cust-006","cust-008"],"spend_365d_total":6200}`}, Status: agent.StepRunning, IdempotencyKey: "run-eino-side-effect:step-1"},
	}
	if registry.OutreachCount() != 0 {
		t.Fatal("side effect happened before the approved Eino Tool was constructed")
	}
	if _, err := runner.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if registry.OutreachCount() != 1 {
		t.Fatalf("repeated approved Eino step was not idempotent: count=%d", registry.OutreachCount())
	}
}

type captureToolExecutor struct {
	calls       int
	toolID, key string
}

func (e *captureToolExecutor) Execute(_ context.Context, toolID string, _ map[string]string, key string) (string, error) {
	e.calls++
	e.toolID, e.key = toolID, key
	return "shared-result", nil
}

type captureToolValidator struct{}

func (captureToolValidator) ValidateInput(string, map[string]string) error { return nil }
func (captureToolValidator) ValidateOutput(string, string) error           { return nil }

// deterministicToolCallingModel is a zero-network ToolCallingChatModel fixture.
// It emits one ToolCall using the tool supplied by ADK, making the ToolNode path
// observable without relying on an SDK mock or provider credentials.
type deterministicToolCallingModel struct {
	tools     []*schema.ToolInfo
	toolName  string
	arguments string
}

func (m *deterministicToolCallingModel) Generate(_ context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	options := einomodel.GetCommonOptions(&einomodel.Options{Tools: m.tools}, opts...)
	for _, message := range input {
		if message != nil && message.Role == schema.Tool {
			return schema.AssistantMessage("tool-result", nil), nil
		}
	}
	if len(options.Tools) == 0 {
		return nil, errors.New("fixture tool list is empty")
	}
	name, args := m.toolName, m.arguments
	if name == "" {
		name = options.Tools[0].Name
	}
	if args == "" {
		args = `{"message":"hello"}`
	}
	return schema.AssistantMessage("", []schema.ToolCall{{ID: "fixture-call-1", Type: "function", Function: schema.FunctionCall{Name: name, Arguments: args}}}), nil
}

func (m *deterministicToolCallingModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	message, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{message}), nil
}

func (m *deterministicToolCallingModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	m.tools = append([]*schema.ToolInfo(nil), tools...)
	return m, nil
}

func TestWorkerRunnerSharedContract(t *testing.T) {
	runners := []struct {
		name string
		make func(error) application.WorkerRunner
	}{
		{name: "function", make: func(err error) application.WorkerRunner {
			return run.AdaptStepRunner{Runner: contractStepRunner{err: err}}
		}},
		{name: "eino", make: func(err error) application.WorkerRunner { return &Runner{runner: &fakeADKRunner{eventErr: err}} }},
	}
	for _, test := range runners {
		t.Run(test.name, func(t *testing.T) {
			runner := test.make(nil)
			request := approvedStepRequest()
			result, err := runner.Run(context.Background(), request)
			if err != nil || result.Content != "contract result" || result.Checkpoint == "" {
				t.Fatalf("runner result = %#v, %v", result, err)
			}
			request.ResumeToken = "run-1:step-1"
			result, err = test.make(nil).Run(context.Background(), request)
			if err != nil || result.Content != "contract result" || result.Checkpoint == "" {
				t.Fatalf("runner resume = %#v, %v", result, err)
			}
			request.ResumeToken = ""
			if _, err := runner.Run(canceledContext(), request); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled context error = %v", err)
			}
			request.Step.ToolID = "unapproved"
			if _, err := runner.Run(context.Background(), request); err == nil {
				t.Fatal("runner accepted an unapproved step")
			}
			if _, err := test.make(application.ErrOutcomeUnknown).Run(context.Background(), approvedStepRequest()); !errors.Is(err, application.ErrOutcomeUnknown) {
				t.Fatalf("unknown outcome was not preserved: %v", err)
			}
			deadline, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancelDeadline()
			if _, err := test.make(nil).Run(deadline, approvedStepRequest()); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("deadline error was not preserved: %v", err)
			}
		})
	}
}

func approvedStepRequest() application.WorkerRequest {
	step := agent.PlanStep{StepID: "step-1", WorkerID: "worker", ToolID: "tool", Status: agent.StepRunning}
	return application.WorkerRequest{RunID: "run-1", WorkerID: "worker", ToolID: "tool", Step: step, Session: application.RunnerSession{Checkpoint: "run-1:step-1"}}
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

type contractStepRunner struct{ err error }

func (r contractStepRunner) RunStep(ctx context.Context, _ run.RunnerSession, _ agent.PlanStep) (run.StepResult, error) {
	if err := ctx.Err(); err != nil {
		return run.StepResult{}, err
	}
	if r.err != nil {
		return run.StepResult{}, r.err
	}
	return run.StepResult{Content: "contract result", Checkpoint: "run-1:step-1"}, nil
}

func (r contractStepRunner) ResumeStep(ctx context.Context, _ run.RunnerSession, token string, _ agent.PlanStep) (run.StepResult, error) {
	if err := ctx.Err(); err != nil {
		return run.StepResult{}, err
	}
	if r.err != nil {
		return run.StepResult{}, r.err
	}
	return run.StepResult{Content: "contract result", Checkpoint: token}, nil
}

type fakeADKRunner struct {
	resumeID string
	queries  int
	eventErr error
}

func (r *fakeADKRunner) Query(context.Context, string, ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	r.queries++
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(&adk.AgentEvent{Err: r.eventErr, Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{Message: &schema.Message{Role: schema.Assistant, Content: "contract result"}}}})
	gen.Close()
	return iter
}

func (r *fakeADKRunner) Resume(_ context.Context, checkpointID string, _ ...adk.AgentRunOption) (*adk.AsyncIterator[*adk.AgentEvent], error) {
	r.resumeID = checkpointID
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(&adk.AgentEvent{Err: r.eventErr, Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{Message: &schema.Message{Role: schema.Assistant, Content: "contract result"}}}})
	gen.Close()
	return iter, nil
}
