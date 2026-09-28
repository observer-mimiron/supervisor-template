// Package composition 只负责创建并连接应用依赖。
//
// 本包不承载路由、状态转换、审批或 Tool 业务判断。
package composition

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/observer-mimiron/supervisor-template/internal/application"
	appmemory "github.com/observer-mimiron/supervisor-template/internal/application/memory"
	runapp "github.com/observer-mimiron/supervisor-template/internal/application/run"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/domain/agent"
	"github.com/observer-mimiron/supervisor-template/internal/domain/operation"
	authinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/auth"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/checkpoint"
	einoinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/eino"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/eventbus"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/examplebusiness"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/llm"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/mcp"
	memoryinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/memory"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/observability"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/persistence"
	mysqlinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/persistence/mysql"
	toolinfra "github.com/observer-mimiron/supervisor-template/internal/infrastructure/tool"
)

// App 是进程级依赖图和启动健康状态。
type App struct {
	Config         config.Config
	Logger         *slog.Logger
	Authenticator  application.Authenticator
	Health         application.HealthService
	Run            *runapp.Service
	Memory         appmemory.Store
	Close          func(context.Context) error
	snapshot       RuntimeSnapshot
	fakeWriteCount func() int
}

// RuntimeSnapshot is the immutable startup capability view used by local
// evaluation evidence. It contains only registered IDs and worker allow-lists.
type RuntimeSnapshot struct {
	ToolIDs     []string
	WorkerTools map[string][]string
}

func (s RuntimeSnapshot) clone() RuntimeSnapshot {
	clone := RuntimeSnapshot{ToolIDs: append([]string(nil), s.ToolIDs...), WorkerTools: make(map[string][]string, len(s.WorkerTools))}
	for workerID, tools := range s.WorkerTools {
		clone.WorkerTools[workerID] = append([]string(nil), tools...)
	}
	return clone
}

// RuntimeSnapshot returns a copy of the startup registration and policy view.
func (a *App) RuntimeSnapshot() RuntimeSnapshot {
	if a == nil {
		return RuntimeSnapshot{}
	}
	return a.snapshot.clone()
}

// FakeWriteCount returns the process-local simulated side-effect count.
func (a *App) FakeWriteCount() int {
	if a == nil || a.fakeWriteCount == nil {
		return 0
	}
	return a.fakeWriteCount()
}

// New 根据已校验配置创建依赖图，并选择内存/文件、fake/真实和 MCP 基础设施实现。
func New(cfg config.Config) (*App, error) {
	catalog, err := cfg.CompileRuntimeCatalog()
	if err != nil {
		return nil, err
	}
	if err := validateExampleBusiness(catalog); err != nil {
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
	var logWriter io.Writer = os.Stderr
	var logCloser io.Closer
	var logDegraded error
	if cfg.Observability.LogFile != "" {
		fileWriter, writerErr := observability.NewRotatingFileWriter(cfg.Observability.LogFile, cfg.Observability.RotateMaxBytes, cfg.Observability.RotateDaily, cfg.Observability.RetentionFiles)
		if writerErr != nil {
			logDegraded = writerErr
		} else {
			logWriter, logCloser = fileWriter, fileWriter
		}
	}
	defer func() {
		if cleanup && logCloser != nil {
			_ = logCloser.Close()
		}
	}()
	logger := observability.NewLogger(cfg.Observability.LogLevel, logWriter)
	observation.SetLogger(logger)
	if logDegraded != nil {
		observation.SignalDegraded(context.Background(), "log_file")
		observability.Log(context.Background(), logger, slog.LevelWarn, "observability.degraded", slog.String("signal", "log_file"), slog.String("error_class", "unavailable"))
	}
	var mysqlAdapter *mysqlinfra.Adapter
	if mysqlConfig := catalog.MySQL(); mysqlConfig.Enabled {
		dsn := os.Getenv(mysqlConfig.DSNEnv)
		if dsn == "" {
			return nil, fmt.Errorf("MySQL DSN 环境变量 %q 未设置", mysqlConfig.DSNEnv)
		}
		mysqlAdapter, err = mysqlinfra.Open(context.Background(), dsn, nil)
		if err != nil {
			return nil, err
		}
		if mysqlConfig.AutoMigrate {
			if err := mysqlAdapter.AutoMigrate(context.Background()); err != nil {
				_ = mysqlAdapter.Close()
				return nil, fmt.Errorf("MySQL 迁移失败: %w", err)
			}
		}
		if mysqlConfig.Seed {
			if err := mysqlAdapter.Seed(context.Background()); err != nil {
				_ = mysqlAdapter.Close()
				return nil, fmt.Errorf("MySQL 种子失败: %w", err)
			}
		}
	}
	defer func() {
		if cleanup && mysqlAdapter != nil {
			_ = mysqlAdapter.Close()
		}
	}()
	var repository application.Repository = persistence.NewMemoryRepository()
	var checkpoints application.CheckpointStore = checkpoint.NewMemoryStore()
	var eventStore application.EventStore = eventbus.NewMemoryBus()
	var memoryStore appmemory.Store = memoryinfra.NewStore()
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
		memoryStore, err = memoryinfra.OpenFileStore(filepath.Join(cfg.Storage.Dir, "memory", "working.json"))
		if err != nil {
			return nil, err
		}
	}
	events := observation.WrapEventStore(eventStore)
	var supervisor application.DecisionProvider
	switch cfg.Model.Provider {
	case "fake":
		supervisor = llm.NewFakeSupervisor(toFakeRoutes(catalog.Routes())...)
	case "deepseek":
		instruction, readErr := os.ReadFile(catalog.Supervisor().PromptFile)
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
	userQuery := catalog.Tools()[examplebusiness.ReadOnlyToolID]
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
	tools, err := toolinfra.NewRegistryWithMCP(userQuery.Implementation, userQuery.Endpoint, userQuery.Timeout, mcpClient, userQuery.MCPServer, mysqlAdapter)
	if err != nil {
		return nil, fmt.Errorf("Tool 装配失败: %w", err)
	}
	workers := make([]operation.WorkerContract, 0, len(cfg.Agent.Workers))
	limits := catalog.Limits()
	budget := application.ExecutionBudget{
		MaxPlanSteps: limits.MaxPlanSteps,
		MaxToolCalls: limits.MaxToolCalls,
		MaxRetries:   limits.MaxRetries,
		CostBudget:   limits.CostBudget,
		Timeout:      userQuery.Timeout,
	}
	for workerID, worker := range catalog.Workers() {
		if worker.Enabled {
			workers = append(workers, operation.WorkerContract{
				WorkerID:       workerID,
				Implementation: worker.Implementation,
				AllowedTools:   append([]string(nil), worker.AllowedTools...),
				Timeout:        worker.Timeout,
			})
			if budget.Timeout <= 0 || (worker.Timeout > 0 && worker.Timeout < budget.Timeout) {
				budget.Timeout = worker.Timeout
			}
		}
	}
	if budget.Timeout <= 0 {
		budget.Timeout = 5 * time.Second
	}
	contracts := toolinfra.ContractsFor(userQuery.Implementation)
	if mysqlAdapter != nil {
		contracts = toolinfra.ContractsForMySQL(userQuery.Implementation)
	}
	policy := agent.NewPolicyGate(workers, contracts)
	authenticator := authinfra.NewStaticBearerAuthenticator(cfg.Auth.Credentials)
	var modelProvider ModelProvider
	if realSupervisor, ok := supervisor.(*llm.RealSupervisor); ok {
		modelProvider = realSupervisor
	}
	runnerFactory := NewRunnerFactory(modelProvider, tools, tools, contracts, einoinfra.NewMemoryCheckpointStore())
	runners := make(map[string]application.WorkerRunner)
	for workerID, worker := range catalog.Workers() {
		if !worker.Enabled {
			continue
		}
		workerRunner, buildErr := runnerFactory.Build(context.Background(), worker)
		if buildErr != nil {
			return nil, fmt.Errorf("Worker %q Runner 装配失败: %w", workerID, buildErr)
		}
		runners[workerID] = workerRunner
	}
	runner, err := NewWorkerRunnerDispatcher(runners)
	if err != nil {
		return nil, err
	}
	deps := application.Dependencies{
		Repository:    repository,
		Checkpoint:    checkpoints,
		EventBus:      events,
		Supervisor:    supervisor,
		Policy:        policy,
		RunAuth:       authinfra.OwnerRunAuthorizer{},
		Runner:        runner,
		Tools:         tools,
		ToolValidator: tools,
		MemoryStore:   memoryStore,
		MemoryRead:    memoryStore,
		Observer:      observation,
		Budget:        budget,
	}
	snapshot := RuntimeSnapshot{WorkerTools: make(map[string][]string)}
	for toolID, tool := range catalog.Tools() {
		if tool.Enabled {
			snapshot.ToolIDs = append(snapshot.ToolIDs, toolID)
		}
	}
	sort.Strings(snapshot.ToolIDs)
	for workerID, worker := range catalog.Workers() {
		if worker.Enabled {
			snapshot.WorkerTools[workerID] = append([]string(nil), worker.AllowedTools...)
			sort.Strings(snapshot.WorkerTools[workerID])
		}
	}
	app := &App{Config: cfg, Logger: logger, Authenticator: authenticator, Run: runapp.NewService(deps), Memory: memoryStore, Health: application.HealthService{Dependencies: deps}, snapshot: snapshot, fakeWriteCount: tools.OutreachCount}
	app.Close = func(shutdownCtx context.Context) error {
		shutdownErr := observation.Shutdown(shutdownCtx)
		if mysqlAdapter != nil {
			shutdownErr = errors.Join(shutdownErr, mysqlAdapter.Close())
		}
		if logCloser != nil {
			shutdownErr = errors.Join(shutdownErr, logCloser.Close())
		}
		return shutdownErr
	}
	if !app.Health.Healthy() {
		return nil, errors.New("应用依赖装配不完整")
	}
	cleanup = false
	return app, nil
}

// toFakeRoutes 将经过启动校验的配置路由转换为 fake Supervisor 的只读快照。
func toFakeRoutes(routes map[string]config.RouteConfig) []llm.FakeRoute {
	converted := make([]llm.FakeRoute, 0, len(routes))
	for _, route := range routes {
		converted = append(converted, llm.FakeRoute{
			WorkerID: route.WorkerID,
			Intent:   route.Intent,
			Matches:  append([]string(nil), route.Matches...),
			ToolID:   route.ToolID,
			Risk:     agent.Risk(route.Risk),
		})
	}
	return converted
}

// validateExampleBusiness confirms that configuration only selects capabilities
// declared by the explicit example module; it does not create new business IDs.
func validateExampleBusiness(catalog config.RuntimeCatalog) error {
	for workerID, worker := range catalog.Workers() {
		if !worker.Enabled {
			continue
		}
		registered, ok := examplebusiness.WorkerFor(workerID)
		runnerOK := worker.Runner == registered.Runner || worker.Runner == "eino_adk"
		if !ok || worker.Implementation != registered.Implementation || !runnerOK || worker.PromptFile != registered.PromptFile {
			return fmt.Errorf("Worker %q 未在示例业务模块注册", workerID)
		}
	}
	for toolID, tool := range catalog.Tools() {
		if tool.Enabled && !examplebusiness.ToolImplementationRegistered(toolID, tool.Implementation) {
			return fmt.Errorf("Tool %q 未在示例业务模块注册", toolID)
		}
	}
	for routeID, route := range catalog.Routes() {
		registered, ok := examplebusiness.RouteFor(routeID)
		if !ok || route.WorkerID != registered.WorkerID || route.ToolID != registered.ToolID || route.Intent != registered.Intent || agent.Risk(route.Risk) != registered.Risk {
			return fmt.Errorf("route %q 未在示例业务模块注册", routeID)
		}
	}
	return nil
}
