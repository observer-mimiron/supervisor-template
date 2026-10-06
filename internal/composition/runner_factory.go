package composition

import (
	"context"
	"fmt"
	"os"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/observer-mimiron/supervisor-template/internal/application"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	domaintool "github.com/observer-mimiron/supervisor-template/internal/domain/tool"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/eino"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
)

// ModelProvider supplies the shared tool-calling model to runner builders.
type ModelProvider interface {
	Model() model.ToolCallingChatModel
}

// RunnerFactory converts a catalog-selected Worker runner into an application
// WorkerRunner. It is the composition equivalent of a per-kind NodeBuilder.
type RunnerFactory struct {
	model      ModelProvider
	tools      application.ToolExecutor
	validator  application.ToolContractValidator
	contracts  []domaintool.Contract
	checkpoint compose.CheckPointStore
	// resolvePath 把配置书写的提示词路径解析成可读的绝对路径。
	resolvePath func(string) string
}

func NewRunnerFactory(modelProvider ModelProvider, tools application.ToolExecutor, validator application.ToolContractValidator, contracts []domaintool.Contract, checkpoint compose.CheckPointStore, resolvePath func(string) string) RunnerFactory {
	if resolvePath == nil {
		resolvePath = func(path string) string { return path }
	}
	return RunnerFactory{model: modelProvider, tools: tools, validator: validator, contracts: contracts, checkpoint: checkpoint, resolvePath: resolvePath}
}

// Build creates exactly one runner for one registered Worker configuration.
func (f RunnerFactory) Build(ctx context.Context, workerID string, worker config.WorkerConfig) (application.WorkerRunner, error) {
	switch worker.Runner {
	case examplebusiness.RunnerID, "":
		return application.SingleToolRunner{Tools: f.tools}, nil
	case examplebusiness.EinoRunnerID:
		if f.model == nil || f.model.Model() == nil {
			return nil, fmt.Errorf("Worker Runner %q 需要 ToolCallingChatModel", worker.Runner)
		}
		if f.checkpoint == nil {
			return nil, fmt.Errorf("Worker Runner %q 缺少 checkpoint store", worker.Runner)
		}
		// 配置声明的 prompt_file 是该 Worker 的模型指令；读取失败按启动错误处理，
		// 与 Supervisor 提示词保持一致，不把失败推迟到请求期。
		instruction, readErr := os.ReadFile(f.resolvePath(worker.PromptFile))
		if readErr != nil {
			return nil, fmt.Errorf("Worker %q 读取 Prompt 失败: %w", workerID, readErr)
		}
		return eino.NewAgentRunner(ctx, f.model.Model(), f.checkpoint, f.contracts, f.tools, f.validator, string(instruction))
	default:
		return nil, fmt.Errorf("Worker Runner %q 未注册", worker.Runner)
	}
}

// WorkerRunnerDispatcher selects the already-built runner by approved WorkerID.
// It does not create state or change the plan; it only dispatches the call.
type WorkerRunnerDispatcher struct {
	runners map[string]application.WorkerRunner
}

func NewWorkerRunnerDispatcher(runners map[string]application.WorkerRunner) (*WorkerRunnerDispatcher, error) {
	if len(runners) == 0 {
		return nil, fmt.Errorf("没有可用 Worker Runner")
	}
	cloned := make(map[string]application.WorkerRunner, len(runners))
	for workerID, runner := range runners {
		if workerID == "" || runner == nil {
			return nil, fmt.Errorf("Worker Runner 注册项不完整")
		}
		cloned[workerID] = runner
	}
	return &WorkerRunnerDispatcher{runners: cloned}, nil
}

func (d *WorkerRunnerDispatcher) Run(ctx context.Context, request application.WorkerRequest) (application.WorkerResult, error) {
	if d == nil {
		return application.WorkerResult{}, fmt.Errorf("Worker Runner Dispatcher 未装配")
	}
	runner, ok := d.runners[request.WorkerID]
	if !ok {
		return application.WorkerResult{}, fmt.Errorf("Worker %q Runner 未注册", request.WorkerID)
	}
	return runner.Run(ctx, request)
}

// RunnerFor exposes the already-built runner for focused composition checks.
func (d *WorkerRunnerDispatcher) RunnerFor(workerID string) (application.WorkerRunner, bool) {
	if d == nil {
		return nil, false
	}
	runner, ok := d.runners[workerID]
	return runner, ok
}

var _ application.WorkerRunner = (*WorkerRunnerDispatcher)(nil)
