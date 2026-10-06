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
	databaseState  func(context.Context) (DatabaseState, error)
}

// DatabaseState is a bounded postcondition projection. It deliberately keeps
// only fields needed to verify the example order contract.
type DatabaseState struct {
	Backend string
	Orders  []DatabaseOrder
}

type DatabaseOrder struct {
	UserID      uint64
	ProductID   uint64
	Quantity    int64
	TotalAmount string
}

// RuntimeSnapshot is the immutable startup capability view used by local
// evaluation evidence. It contains registered IDs, worker allow-lists and
// the safety metadata needed to classify Tool calls without naming a business Tool.
type ToolMetadata struct {
	Risk                string `json:"risk"`
	RequiresApproval    bool   `json:"requires_approval"`
	IdempotencyRequired bool   `json:"idempotency_required"`
}

type RuntimeSnapshot struct {
	ToolIDs      []string
	WorkerTools  map[string][]string
	ToolMetadata map[string]ToolMetadata
}

func (s RuntimeSnapshot) clone() RuntimeSnapshot {
	clone := RuntimeSnapshot{
		ToolIDs:      append([]string(nil), s.ToolIDs...),
		WorkerTools:  make(map[string][]string, len(s.WorkerTools)),
		ToolMetadata: make(map[string]ToolMetadata, len(s.ToolMetadata)),
	}
	for workerID, tools := range s.WorkerTools {
		clone.WorkerTools[workerID] = append([]string(nil), tools...)
	}
	for toolID, metadata := range s.ToolMetadata {
		clone.ToolMetadata[toolID] = metadata
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

// DatabaseState returns the optional MySQL projection used by local cases.
// Memory-only runs return an unavailable backend without touching business
// state.
func (a *App) DatabaseState(ctx context.Context) (DatabaseState, error) {
	if a == nil || a.databaseState == nil {
		return DatabaseState{}, nil
	}
	return a.databaseState(ctx)
}

// New 根据已校验配置创建依赖图，并选择内存/文件、fake/真实和 MCP 基础设施实现。
func New(cfg config.Config) (*App, error) {
	catalog, err := cfg.CompileRuntimeCatalog(exampleBusinessRegistration())
	if err != nil {
		return nil, err
	}
	observation, err := observability.Setup(context.Background(), observabilityOptions(cfg.Observability))
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
		fileWriter, writerErr := observability.NewRotatingFileWriter(cfg.Observability.LogFile, cfg.Observability.RotateMaxBytes, cfg.Observability.RotateDaily, cfg.Observability.RetentionFiles, cfg.Observability.FilePermissions)
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
	var mysqlConnection *mysqlinfra.Connection
	var mysqlOrders *mysqlinfra.OrderToolAdapter
	var mysqlLeases *mysqlinfra.RunLeaseAdapter
	var databaseState func(context.Context) (DatabaseState, error)
	if mysqlConfig := catalog.MySQL(); mysqlConfig.Enabled {
		dsn := os.Getenv(mysqlConfig.DSNEnv)
		if dsn == "" {
			return nil, fmt.Errorf("MySQL DSN 环境变量 %q 未设置", mysqlConfig.DSNEnv)
		}
		mysqlConnection, err = mysqlinfra.Open(context.Background(), dsn, observation.TracerProvider(), logger)
		if err != nil {
			return nil, err
		}
		mysqlOrders = mysqlConnection.NewOrderToolAdapter()
		mysqlLeases = mysqlConnection.NewRunLeaseAdapter()
		databaseState = func(ctx context.Context) (DatabaseState, error) {
			orders, snapshotErr := mysqlOrders.Snapshot(ctx)
			state := DatabaseState{Backend: "mysql", Orders: make([]DatabaseOrder, 0, len(orders))}
			for _, order := range orders {
				state.Orders = append(state.Orders, DatabaseOrder{UserID: order.UserID, ProductID: order.ProductID, Quantity: order.Quantity, TotalAmount: order.TotalAmount})
			}
			return state, snapshotErr
		}
		if mysqlConfig.AutoMigrate {
			if err := mysqlOrders.AutoMigrate(context.Background()); err != nil {
				_ = mysqlConnection.Close()
				return nil, fmt.Errorf("MySQL 迁移失败: %w", err)
			}
			if err := mysqlLeases.AutoMigrate(context.Background()); err != nil {
				_ = mysqlConnection.Close()
				return nil, fmt.Errorf("MySQL lease 迁移失败: %w", err)
			}
		}
		if mysqlConfig.Seed {
			if err := mysqlOrders.Seed(context.Background()); err != nil {
				_ = mysqlConnection.Close()
				return nil, fmt.Errorf("MySQL 种子失败: %w", err)
			}
		}
	}
	defer func() {
		if cleanup && mysqlConnection != nil {
			_ = mysqlConnection.Close()
		}
	}()
	var repository application.Repository = persistence.NewMemoryRepository()
	var checkpoints application.CheckpointStore = checkpoint.NewMemoryStore()
	var eventStore application.EventStore = eventbus.NewMemoryBus()
	var leases application.RunLeaseStore = persistence.NewMemoryRunLeaseStore()
	var memoryStore appmemory.Store = memoryinfra.NewStore()
	if cfg.Storage.Backend == "file" {
		repository, err = persistence.NewFileRepository(filepath.Join(cfg.Storage.Dir, "runs"))
		if err != nil {
			return nil, err
		}
		checkpoints, err = checkpoint.NewFileStore(filepath.Join(cfg.Storage.Dir, "checkpoints"), cfg.Checkpoint.Retention)
		if err != nil {
			return nil, err
		}
		fileEvents, fileErr := eventbus.NewFileBus(filepath.Join(cfg.Storage.Dir, "events"))
		if fileErr != nil {
			return nil, fileErr
		}
		eventStore = fileEvents
		leases, err = persistence.NewFileRunLeaseStore(filepath.Join(cfg.Storage.Dir, "leases"))
		if err != nil {
			return nil, err
		}
		memoryStore, err = memoryinfra.OpenFileStore(filepath.Join(cfg.Storage.Dir, "memory", "working.json"))
		if err != nil {
			return nil, err
		}
	}
	events := observation.WrapEventStore(eventStore)
	var supervisor application.DecisionProvider
	switch cfg.Model.Provider {
	case "fake":
		supervisor = llm.NewFakeSupervisorWithBuilder(toFakeRoutes(catalog.Routes()), examplebusiness.FakeDecisionBuilder)
	case "deepseek":
		instruction, readErr := os.ReadFile(cfg.ResolvePath(catalog.Supervisor().PromptFile))
		if readErr != nil {
			return nil, fmt.Errorf("读取 Supervisor Prompt 失败: %w", readErr)
		}
		supervisor, err = llm.NewDeepSeekSupervisor(context.Background(), llm.ModelOptions{
			Name: cfg.Model.Name, BaseURL: cfg.Model.BaseURL, APIKeyEnv: cfg.Model.APIKeyEnv,
			Temperature: cfg.Model.Temperature, MaxTokens: cfg.Model.MaxTokens, Timeout: cfg.Model.Timeout,
			MaxSteps: catalog.Supervisor().MaxSteps,
		}, string(instruction))
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
	contracts := examplebusiness.ToolContracts(userQuery.Implementation)
	if mysqlOrders != nil {
		contracts = examplebusiness.ToolContractsWithMySQL(userQuery.Implementation)
		leases = mysqlLeases
	}
	tools, err := toolinfra.NewRegistry(userQuery.Timeout, contracts)
	if err != nil {
		return nil, fmt.Errorf("Tool 装配失败: %w", err)
	}
	fakeRuntime := examplebusiness.NewFakeRuntime()
	// fake_delay_ms 只影响 fake 实现：让某一步保持"仍在执行"，验收用例才能在其中取消。
	fakeRuntime.SetToolDelay(examplebusiness.ReadOnlyToolID, time.Duration(userQuery.FakeDelayMS)*time.Millisecond)
	fakeRuntime.SetToolDelay(examplebusiness.SummaryToolID, time.Duration(catalog.Tools()[examplebusiness.SummaryToolID].FakeDelayMS)*time.Millisecond)
	fakeRuntime.SetToolDelay(examplebusiness.SideEffectToolID, time.Duration(catalog.Tools()[examplebusiness.SideEffectToolID].FakeDelayMS)*time.Millisecond)
	if err := tools.RegisterHandler(examplebusiness.SummaryToolID, func(ctx context.Context, input map[string]string, key string) (string, error) {
		return fakeRuntime.Execute(ctx, examplebusiness.SummaryToolID, input, key)
	}); err != nil {
		return nil, fmt.Errorf("Summary Tool 装配失败: %w", err)
	}
	if err := tools.RegisterHandler(examplebusiness.SideEffectToolID, func(ctx context.Context, input map[string]string, key string) (string, error) {
		return fakeRuntime.Execute(ctx, examplebusiness.SideEffectToolID, input, key)
	}); err != nil {
		return nil, fmt.Errorf("副作用 Tool 装配失败: %w", err)
	}
	switch userQuery.Implementation {
	case "", examplebusiness.ReadOnlyToolFake:
		if err := tools.RegisterHandler(examplebusiness.ReadOnlyToolID, func(ctx context.Context, input map[string]string, key string) (string, error) {
			return fakeRuntime.Execute(ctx, examplebusiness.ReadOnlyToolID, input, key)
		}); err != nil {
			return nil, fmt.Errorf("查询 Tool 装配失败: %w", err)
		}
	case examplebusiness.ReadOnlyToolHTTP:
		readOnly, buildErr := toolinfra.NewHTTPReadOnlyTool(userQuery.Endpoint, userQuery.Timeout)
		if buildErr != nil {
			return nil, buildErr
		}
		if err := tools.RegisterHandler(examplebusiness.ReadOnlyToolID, func(ctx context.Context, input map[string]string, _ string) (string, error) {
			return readOnly.Execute(ctx, input)
		}); err != nil {
			return nil, fmt.Errorf("HTTP Tool 装配失败: %w", err)
		}
	case examplebusiness.ReadOnlyToolMCP:
		if mcpClient == nil || userQuery.MCPServer == "" {
			return nil, errors.New("MCP Tool 未配置 client 或 server")
		}
		if err := tools.RegisterHandler(examplebusiness.ReadOnlyToolID, func(ctx context.Context, input map[string]string, _ string) (string, error) {
			return mcpClient.Call(ctx, userQuery.MCPServer, examplebusiness.ReadOnlyToolID, input)
		}); err != nil {
			return nil, fmt.Errorf("MCP Tool 装配失败: %w", err)
		}
	default:
		return nil, fmt.Errorf("Tool 实现 %q 未注册", userQuery.Implementation)
	}
	if mysqlOrders != nil {
		if err := tools.RegisterHandler(examplebusiness.MySQLQueryToolID, func(ctx context.Context, input map[string]string, _ string) (string, error) {
			return mysqlOrders.Query(ctx, []byte(input["message"]))
		}); err != nil {
			return nil, fmt.Errorf("MySQL 查询 Tool 装配失败: %w", err)
		}
		if err := tools.RegisterHandler(examplebusiness.MySQLInsertToolID, func(ctx context.Context, input map[string]string, key string) (string, error) {
			return mysqlOrders.Insert(ctx, []byte(input["message"]), key)
		}); err != nil {
			return nil, fmt.Errorf("MySQL 写入 Tool 装配失败: %w", err)
		}
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
	policy := agent.NewPolicyGate(selectPolicyWorkers(workers, catalog.Supervisor().AllowedWorkers), contracts)
	authCredentials := make([]authinfra.Credential, 0, len(cfg.Auth.Credentials))
	for _, credential := range cfg.Auth.Credentials {
		authCredentials = append(authCredentials, authinfra.Credential{
			TokenSHA256Env: credential.TokenSHA256Env,
			TenantID:       credential.TenantID,
			SubjectID:      credential.SubjectID,
		})
	}
	authenticator := authinfra.NewStaticBearerAuthenticator(authCredentials)
	var modelProvider ModelProvider
	if realSupervisor, ok := supervisor.(*llm.RealSupervisor); ok {
		modelProvider = realSupervisor
	}
	runnerFactory := NewRunnerFactory(modelProvider, tools, tools, contracts, einoinfra.NewMemoryCheckpointStore(), cfg.ResolvePath)
	runners := make(map[string]application.WorkerRunner)
	for workerID, worker := range catalog.Workers() {
		if !worker.Enabled {
			continue
		}
		workerRunner, buildErr := runnerFactory.Build(context.Background(), workerID, worker)
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
		Repository:      repository,
		Checkpoint:      checkpoints,
		EventBus:        events,
		Leases:          leases,
		Supervisor:      supervisor,
		Policy:          policy,
		RunAuth:         authinfra.OwnerRunAuthorizer{},
		Runner:          runner,
		ToolValidator:   tools,
		MemoryStore:     memoryStore,
		MemoryRead:      memoryStore,
		ToolRetryLimits: toolRetryLimits(catalog.Tools(), limits.MaxRetries),
		ApprovalTimeout: cfg.Approval.Timeout,
		Observer:        observation,
		Budget:          budget,
	}
	snapshot := RuntimeSnapshot{WorkerTools: make(map[string][]string), ToolMetadata: make(map[string]ToolMetadata)}
	for toolID, tool := range catalog.Tools() {
		if tool.Enabled {
			snapshot.ToolIDs = append(snapshot.ToolIDs, toolID)
		}
	}
	for _, contract := range contracts {
		snapshot.ToolMetadata[contract.ToolID] = ToolMetadata{
			Risk:                contract.Risk,
			RequiresApproval:    contract.RequiresApproval,
			IdempotencyRequired: contract.IdempotencyRequired,
		}
	}
	sort.Strings(snapshot.ToolIDs)
	for workerID, worker := range catalog.Workers() {
		if worker.Enabled {
			snapshot.WorkerTools[workerID] = append([]string(nil), worker.AllowedTools...)
			sort.Strings(snapshot.WorkerTools[workerID])
		}
	}
	app := &App{Config: cfg, Logger: logger, Authenticator: authenticator, Run: runapp.NewService(deps), Memory: memoryStore, Health: application.HealthService{Dependencies: deps}, snapshot: snapshot, fakeWriteCount: fakeRuntime.OutreachCount, databaseState: databaseState}
	app.Close = func(shutdownCtx context.Context) error {
		var shutdownErr error
		if mysqlConnection != nil {
			shutdownErr = errors.Join(shutdownErr, mysqlConnection.Close())
		}
		if logCloser != nil {
			shutdownErr = errors.Join(shutdownErr, logCloser.Close())
		}
		shutdownErr = errors.Join(shutdownErr, observation.Shutdown(shutdownCtx))
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

func exampleBusinessRegistration() config.RegistrationSnapshot {
	registration := config.RegistrationSnapshot{
		Workers: make(map[string]config.WorkerRegistration),
		Tools:   make(map[string]config.ToolRegistration),
		Routes:  make(map[string]config.RouteRegistration),
	}
	for _, worker := range examplebusiness.Workers() {
		runners := []string{worker.Runner, examplebusiness.EinoRunnerID}
		registration.Workers[worker.WorkerID] = config.WorkerRegistration{
			Implementation: worker.Implementation,
			Runners:        runners,
			PromptFile:     worker.PromptFile,
		}
	}
	for _, toolID := range examplebusiness.ToolIDs() {
		registration.Tools[toolID] = config.ToolRegistration{
			Implementations: examplebusiness.ToolImplementations(toolID),
		}
	}
	for _, route := range examplebusiness.Routes() {
		registration.Routes[route.ID] = config.RouteRegistration{
			WorkerID: route.WorkerID,
			Intent:   route.Intent,
			ToolID:   route.ToolID,
			Risk:     string(route.Risk),
		}
	}
	return registration
}

func observabilityOptions(cfg config.ObservabilityConfig) observability.Options {
	return observability.Options{
		Enabled: cfg.Enabled, Endpoint: cfg.Endpoint, ServiceName: cfg.ServiceName,
		Insecure: cfg.Insecure, TraceSampleRate: cfg.TraceSampleRate,
		MetricsEnabled: cfg.MetricsEnabled, TraceFile: cfg.TraceFile,
		RotateMaxBytes: cfg.RotateMaxBytes, RotateDaily: cfg.RotateDaily,
		RetentionFiles: cfg.RetentionFiles, ResourceAttributes: cfg.ResourceAttributes,
		LangfuseEnabled: cfg.LangfuseEnabled, LangfuseEndpoint: cfg.LangfuseEndpoint,
		LangfuseHeadersEnv: cfg.LangfuseHeadersEnv, FileMode: cfg.FilePermissions,
	}
}

// selectPolicyWorkers 把 Supervisor 声明的 allowed_workers 变成真实的运行期边界。
//
// 名单为空表示不额外收紧（所有已启用 Worker 都可被路由）；名单非空时，Policy Gate
// 只认识名单内的 Worker，模型提出名单外的 Worker 会被当作未注册而拒绝。
func selectPolicyWorkers(workers []operation.WorkerContract, allowed []string) []operation.WorkerContract {
	if len(allowed) == 0 {
		return workers
	}
	permitted := make(map[string]struct{}, len(allowed))
	for _, workerID := range allowed {
		permitted[workerID] = struct{}{}
	}
	selected := make([]operation.WorkerContract, 0, len(permitted))
	for _, worker := range workers {
		if _, ok := permitted[worker.WorkerID]; ok {
			selected = append(selected, worker)
		}
	}
	return selected
}

// toolRetryLimits 把每个 Tool 配置的重试上限解析成运行期表。
//
// 未配置的 Tool 不出现在表中，运行期回落到 limits.max_retries；显式配置的值优先，
// 因此可以给写操作单独设成 0（永不重试）。负数视为回落全局值。
func toolRetryLimits(tools map[string]config.ToolConfig, fallback int) map[string]int {
	limits := make(map[string]int)
	for toolID, tool := range tools {
		if tool.MaxRetries == nil {
			continue
		}
		value := *tool.MaxRetries
		if value < 0 {
			value = fallback
		}
		limits[toolID] = value
	}
	return limits
}
