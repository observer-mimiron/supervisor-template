package composition

import (
	"context"
	"strings"
	"testing"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	einoinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/eino"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
	toolinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/tool"
)

func TestRunnerFactoryBuildsSingleToolRunner(t *testing.T) {
	factory := NewRunnerFactory(nil, fakeExecutor{}, fakeValidator{}, nil, nil)
	runner, err := factory.Build(context.Background(), config.WorkerConfig{Runner: "single_tool"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.(application.SingleToolRunner); !ok {
		t.Fatalf("runner type = %T", runner)
	}
}

func TestWorkerRunnerDispatcherRoutesByWorkerID(t *testing.T) {
	dispatcher, err := NewWorkerRunnerDispatcher(map[string]application.WorkerRunner{
		"worker-a": recordingRunner{content: "a"},
		"worker-b": recordingRunner{content: "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Run(context.Background(), application.WorkerRequest{WorkerID: "worker-b"})
	if err != nil || result.Content != "b" {
		t.Fatalf("result = %#v, err=%v", result, err)
	}
	if _, err := dispatcher.Run(context.Background(), application.WorkerRequest{WorkerID: "missing"}); err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("unknown worker error = %v", err)
	}
}

func TestRunnerFactoryRejectsIncompleteEinoWiring(t *testing.T) {
	factory := NewRunnerFactory(nil, fakeExecutor{}, fakeValidator{}, nil, nil)
	if _, err := factory.Build(context.Background(), config.WorkerConfig{Runner: examplebusiness.EinoRunnerID}); err == nil || !strings.Contains(err.Error(), "ToolCallingChatModel") {
		t.Fatalf("missing model error = %v", err)
	}
	factory = NewRunnerFactory(modelProviderStub{model: stubChatModel{}}, fakeExecutor{}, fakeValidator{}, nil, nil)
	if _, err := factory.Build(context.Background(), config.WorkerConfig{Runner: examplebusiness.EinoRunnerID}); err == nil || !strings.Contains(err.Error(), "checkpoint") {
		t.Fatalf("missing checkpoint error = %v", err)
	}
}

func TestRunnerFactoryBuildsEinoRunnerWithApprovedDependencies(t *testing.T) {
	factory := NewRunnerFactory(modelProviderStub{model: stubChatModel{}}, fakeExecutor{}, fakeValidator{}, toolinfra.Contracts(), einoinfra.NewMemoryCheckpointStore())
	runner, err := factory.Build(context.Background(), config.WorkerConfig{Runner: examplebusiness.EinoRunnerID})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := runner.(*einoinfra.Runner); !ok {
		t.Fatalf("runner type = %T, want *eino.Runner", runner)
	}
}

type modelProviderStub struct {
	model einomodel.ToolCallingChatModel
}

func (s modelProviderStub) Model() einomodel.ToolCallingChatModel { return s.model }

type stubChatModel struct{}

func (stubChatModel) Generate(context.Context, []*schema.Message, ...einomodel.Option) (*schema.Message, error) {
	return schema.AssistantMessage("", nil), nil
}
func (stubChatModel) Stream(context.Context, []*schema.Message, ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", nil)}), nil
}
func (s stubChatModel) WithTools([]*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return s, nil
}

type recordingRunner struct{ content string }

func (r recordingRunner) Run(context.Context, application.WorkerRequest) (application.WorkerResult, error) {
	return application.WorkerResult{Content: r.content}, nil
}

type fakeExecutor struct{}

func (fakeExecutor) Execute(context.Context, string, map[string]string, string) (string, error) {
	return "", nil
}

type fakeValidator struct{}

func (fakeValidator) ValidateInput(string, map[string]string) error { return nil }
func (fakeValidator) ValidateOutput(string, string) error           { return nil }
