// Package composition 只负责创建并连接应用依赖。
//
// 本包不承载路由、状态转换、审批或 Tool 业务判断。
package composition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	runapp "github.com/observer-mimiron/supervisor-template/internal/application/run"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/operation"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/checkpoint"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/eventbus"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/llm"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/mcp"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/observability"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/persistence"
	toolinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/tool"
)

// App 是进程级依赖图和启动健康状态。
type App struct {
	Config config.Config
	Health application.HealthService
	Run    *runapp.Service
	Close  func(context.Context) error
}

// New 根据已校验配置创建依赖图，并选择内存/文件、fake/真实和 MCP 基础设施实现。
func New(cfg config.Config) (*App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	observation, err := observability.Setup(context.Background(), cfg.Observability)
	if err != nil {
		return nil, fmt.Errorf("观测装配失败: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = observation.Shutdown(context.Background())
		}
	}()
	var repository application.Repository = persistence.NewMemoryRepository()
	var checkpoints application.CheckpointStore = checkpoint.NewMemoryStore()
	var eventStore application.EventStore = eventbus.NewMemoryBus()
	if cfg.Storage.Backend == "file" {
		repository, err = persistence.NewFileRepository(filepath.Join(cfg.Storage.Dir, "runs"))
		if err != nil {
			return nil, err
		}
		checkpoints, err = checkpoint.NewFileStore(filepath.Join(cfg.Storage.Dir, "checkpoints"))
		if err != nil {
			return nil, err
		}
		fileEvents, fileErr := eventbus.NewFileBus(filepath.Join(cfg.Storage.Dir, "events"))
		if fileErr != nil {
			return nil, fileErr
		}
		eventStore = fileEvents
	}
	events := observation.WrapEventStore(eventStore)
	var supervisor application.DecisionProvider
	switch cfg.Model.Provider {
	case "fake":
		supervisor = llm.NewFakeSupervisor()
	case "deepseek":
		instruction, readErr := os.ReadFile(cfg.Agent.Supervisor.PromptFile)
		if readErr != nil {
			return nil, fmt.Errorf("读取 Supervisor Prompt 失败: %w", readErr)
		}
		supervisor, err = llm.NewDeepSeekSupervisor(context.Background(), cfg.Model, string(instruction))
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("model provider %q 未注册", cfg.Model.Provider)
	}
	userQuery := cfg.Tools["user_query"]
	var mcpClient *mcp.Client
	if cfg.MCP.Enabled {
		servers := make(map[string]mcp.Server, len(cfg.MCP.Servers))
		for serverID, server := range cfg.MCP.Servers {
			servers[serverID] = mcp.Server{Endpoint: server.Endpoint, AllowedTools: server.AllowedTools}
		}
		mcpClient, err = mcp.NewClient(servers, userQuery.Timeout)
		if err != nil {
			return nil, fmt.Errorf("MCP 装配失败: %w", err)
		}
	}
	tools, err := toolinfra.NewRegistryWithMCP(userQuery.Implementation, userQuery.Endpoint, userQuery.Timeout, mcpClient, userQuery.MCPServer)
	if err != nil {
		return nil, fmt.Errorf("Tool 装配失败: %w", err)
	}
	workers := make([]operation.WorkerContract, 0, len(cfg.Agent.Workers))
	for workerID, worker := range cfg.Agent.Workers {
		if worker.Enabled {
			workers = append(workers, operation.WorkerContract{
				WorkerID:       workerID,
				Implementation: worker.Implementation,
				AllowedTools:   append([]string(nil), worker.AllowedTools...),
				Timeout:        worker.Timeout,
			})
		}
	}
	policy := agent.NewPolicyGate(workers, toolinfra.ContractsFor(userQuery.Implementation))
	deps := application.Dependencies{
		Repository: repository,
		Checkpoint: checkpoints,
		EventBus:   events,
		Supervisor: supervisor,
		Policy:     policy,
		Tools:      tools,
	}
	app := &App{Config: cfg, Run: runapp.NewService(deps), Health: application.HealthService{Dependencies: deps}, Close: observation.Shutdown}
	if !app.Health.Healthy() {
		return nil, errors.New("应用依赖装配不完整")
	}
	cleanup = false
	return app, nil
}
