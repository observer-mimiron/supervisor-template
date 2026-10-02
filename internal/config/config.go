// Package config 负责读取部署配置并在启动前固定可运行边界。
//
// 本包只处理 TOML、环境变量覆盖和确定性校验，不创建业务对象，也不连接外部服务。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config 是进程启动所需的 typed 配置快照。
type Config struct {
	Server        ServerConfig          `toml:"server"`
	Model         ModelConfig           `toml:"model"`
	Agent         AgentConfig           `toml:"agent"`
	Auth          AuthConfig            `toml:"auth"`
	Tools         map[string]ToolConfig `toml:"tools"`
	Limits        LimitsConfig          `toml:"limits"`
	Approval      ApprovalConfig        `toml:"approval"`
	Checkpoint    CheckpointConfig      `toml:"checkpoint"`
	Storage       StorageConfig         `toml:"storage"`
	MCP           MCPConfig             `toml:"mcp"`
	MySQL         MySQLConfig           `toml:"mysql"`
	Observability ObservabilityConfig   `toml:"observability"`
}

// ServerConfig 定义 HTTP 进程的基础参数。
type ServerConfig struct {
	ListenAddr       string        `toml:"listen_addr"`
	ReadTimeout      time.Duration `toml:"-"`
	WriteTimeout     time.Duration `toml:"-"`
	IdleTimeout      time.Duration `toml:"-"`
	RequestBodyLimit int64         `toml:"request_body_limit"`
	SSEHeartbeat     time.Duration `toml:"-"`
	ReadTimeoutText  string        `toml:"read_timeout"`
	WriteTimeoutText string        `toml:"write_timeout"`
	IdleTimeoutText  string        `toml:"idle_timeout"`
	SSEHeartbeatText string        `toml:"sse_heartbeat"`
}

// ModelConfig 描述模型实现选择和非敏感连接参数；密钥只从 APIKeyEnv 指向的环境变量读取。
type ModelConfig struct {
	Provider    string        `toml:"provider"`
	Name        string        `toml:"name"`
	BaseURL     string        `toml:"base_url"`
	APIKeyEnv   string        `toml:"api_key_env"`
	Temperature float64       `toml:"temperature"`
	MaxTokens   int           `toml:"max_tokens"`
	Timeout     time.Duration `toml:"-"`
	TimeoutText string        `toml:"timeout"`
}

// AgentConfig 描述已注册 Supervisor 和 Worker 的配置选择。
type AgentConfig struct {
	Supervisor SupervisorConfig        `toml:"supervisor"`
	Workers    map[string]WorkerConfig `toml:"workers"`
	Routes     map[string]RouteConfig  `toml:"routes"`
}

// AuthConfig 选择启动时固定的身份认证实现。
// 凭证本身只以哈希形式保存在环境变量，不写入 TOML。
type AuthConfig struct {
	Implementation string             `toml:"implementation"`
	Credentials    []BearerCredential `toml:"credentials"`
}

// BearerCredential 将一个 Bearer token 的 SHA-256 哈希绑定到固定主体。
type BearerCredential struct {
	TokenSHA256Env string `toml:"token_sha256_env"`
	TenantID       string `toml:"tenant_id"`
	SubjectID      string `toml:"subject_id"`
}

// SupervisorConfig 描述 Supervisor 的实现和能力白名单。
type SupervisorConfig struct {
	Enabled        bool     `toml:"enabled"`
	Implementation string   `toml:"implementation"`
	PromptFile     string   `toml:"prompt_file"`
	AllowedWorkers []string `toml:"allowed_workers"`
	MaxSteps       int      `toml:"max_steps"`
}

// WorkerConfig 描述单个 Worker 的实现和 Tool 白名单。
type WorkerConfig struct {
	Enabled        bool          `toml:"enabled"`
	Implementation string        `toml:"implementation"`
	Runner         string        `toml:"runner"`
	PromptFile     string        `toml:"prompt_file"`
	AllowedTools   []string      `toml:"allowed_tools"`
	Timeout        time.Duration `toml:"-"`
	TimeoutText    string        `toml:"timeout"`
}

// RouteConfig 描述一条配置路由；它只能引用已注册 Worker 和 Tool。
type RouteConfig struct {
	WorkerID string   `toml:"worker"`
	Intent   string   `toml:"intent"`
	Matches  []string `toml:"matches"`
	ToolID   string   `toml:"tool"`
	Risk     string   `toml:"risk"`
}

// ToolConfig 描述一个已注册 Tool 的风险和执行限制。
type ToolConfig struct {
	Enabled          bool          `toml:"enabled"`
	Implementation   string        `toml:"implementation"`
	Endpoint         string        `toml:"endpoint"`
	MCPServer        string        `toml:"mcp_server"`
	Risk             string        `toml:"risk"`
	RequiresApproval bool          `toml:"requires_approval"`
	Timeout          time.Duration `toml:"-"`
	MaxRetries       int           `toml:"max_retries"`
	TimeoutText      string        `toml:"timeout"`
	// FakeDelayMS makes the fake implementation of this Tool take at least this
	// long before answering. It only affects fake implementations; real ones
	// ignore it. A deliberately slow step is what lets an acceptance Case cancel
	// a Run while it is still executing.
	FakeDelayMS int `toml:"fake_delay_ms"`
}

// LimitsConfig 固定执行预算的上限。
type LimitsConfig struct {
	MaxPlanSteps int `toml:"max_plan_steps"`
	MaxToolCalls int `toml:"max_tool_calls"`
	MaxRetries   int `toml:"max_retries"`
	CostBudget   int `toml:"cost_budget"`
}

// ApprovalConfig 定义审批和幂等窗口。
type ApprovalConfig struct {
	Timeout           time.Duration `toml:"-"`
	IdempotencyWindow time.Duration `toml:"-"`
	TimeoutText       string        `toml:"timeout"`
	IdempotencyText   string        `toml:"idempotency_window"`
}

// CheckpointConfig 定义内存 checkpoint 的保留时间。
type CheckpointConfig struct {
	Retention     time.Duration `toml:"-"`
	RetentionText string        `toml:"retention"`
}

// StorageConfig 选择内存或按 run 文件持久化的基础设施实现。
type StorageConfig struct {
	Backend string `toml:"backend"`
	Dir     string `toml:"dir"`
}

// MCPConfig 定义启动时注册的 MCP 服务端；默认关闭且不允许动态发现。
type MCPConfig struct {
	Enabled bool                       `toml:"enabled"`
	Servers map[string]MCPServerConfig `toml:"servers"`
}

// MCPServerConfig 描述 MCP endpoint 及其固定工具 allow-list。
type MCPServerConfig struct {
	Endpoint     string   `toml:"endpoint"`
	AllowedTools []string `toml:"allowed_tools"`
}

// MySQLConfig selects the optional local MySQL example adapter. The DSN is
// always read from the named environment variable and is never stored in TOML.
type MySQLConfig struct {
	Enabled     bool   `toml:"enabled"`
	DSNEnv      string `toml:"dsn_env"`
	AutoMigrate bool   `toml:"auto_migrate"`
	Seed        bool   `toml:"seed"`
}

// ObservabilityConfig 定义可选的 OpenTelemetry 导出参数；认证头只从标准环境变量读取。
type ObservabilityConfig struct {
	Enabled            bool              `toml:"enabled"`
	Endpoint           string            `toml:"endpoint"`
	ServiceName        string            `toml:"service_name"`
	Insecure           bool              `toml:"insecure"`
	LogLevel           string            `toml:"log_level"`
	TraceSampleRate    float64           `toml:"trace_sample_rate"`
	MetricsEnabled     bool              `toml:"metrics_enabled"`
	LogFile            string            `toml:"log_file"`
	TraceFile          string            `toml:"trace_file"`
	FileMode           string            `toml:"file_mode"`
	RotateMaxBytes     int64             `toml:"rotate_max_bytes"`
	RotateDaily        bool              `toml:"rotate_daily"`
	RetentionFiles     int               `toml:"retention_files"`
	ResourceAttributes map[string]string `toml:"resource_attributes"`
	LangfuseEnabled    bool              `toml:"langfuse_enabled"`
	LangfuseEndpoint   string            `toml:"langfuse_endpoint"`
	LangfuseHeadersEnv string            `toml:"langfuse_headers_env"`
}

// Load 从 TOML 文件加载配置，再应用环境变量覆盖并执行启动校验。
func Load(path string) (Config, error) {
	return load(path, false)
}

// LoadWithFake 在环境变量覆盖后、启动校验前固定使用本地 fake 模型。
// 这是 cmd/server -fake 的最后模型覆盖，不改变其他配置字段。
func LoadWithFake(path string) (Config, error) {
	return load(path, true)
}

func load(path string, forceFake bool) (Config, error) {
	var cfg Config
	if path == "" {
		return cfg, errors.New("配置文件路径不能为空")
	}
	metadata, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return cfg, fmt.Errorf("读取配置文件失败: %w", err)
	}
	if unknown := metadata.Undecoded(); len(unknown) > 0 {
		return cfg, fmt.Errorf("配置包含未识别字段: %s", formatUndecodedKeys(unknown))
	}
	if err := cfg.applyEnvironment(forceFake); err != nil {
		return cfg, err
	}
	if forceFake {
		cfg.Model.Provider = "fake"
		cfg.Model.Name = "fake-model"
	}
	if err := cfg.parseDurations(); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func formatUndecodedKeys(keys []toml.Key) string {
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, key.String())
	}
	return strings.Join(values, ", ")
}

// Validate 确保配置只能选择已注册实现，且预算和时间边界有效。
func (c Config) Validate() error {
	if c.Auth.Implementation != "static_bearer" {
		return fmt.Errorf("auth implementation %q 未注册", c.Auth.Implementation)
	}
	if len(c.Auth.Credentials) == 0 {
		return errors.New("auth 必须配置至少一个 Bearer 凭证")
	}
	for index, credential := range c.Auth.Credentials {
		if strings.TrimSpace(credential.TokenSHA256Env) == "" || strings.TrimSpace(credential.TenantID) == "" || strings.TrimSpace(credential.SubjectID) == "" {
			return fmt.Errorf("auth credential %d 不完整", index)
		}
	}
	if backend := strings.TrimSpace(c.Storage.Backend); backend != "" && backend != "memory" && backend != "file" {
		return fmt.Errorf("storage.backend %q 未注册", backend)
	}
	if strings.TrimSpace(c.Storage.Backend) == "file" && strings.TrimSpace(c.Storage.Dir) == "" {
		return errors.New("storage.dir 不能为空")
	}
	if c.MCP.Enabled {
		if len(c.MCP.Servers) == 0 {
			return errors.New("MCP 已启用但没有注册服务器")
		}
		for serverID, server := range c.MCP.Servers {
			parsed, err := url.Parse(strings.TrimSpace(server.Endpoint))
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("MCP server %q endpoint 必须是 http/https 地址", serverID)
			}
			if len(server.AllowedTools) == 0 {
				return fmt.Errorf("MCP server %q 必须配置工具 allow-list", serverID)
			}
			if err := rejectDuplicateIDs(fmt.Sprintf("MCP server %q allowed_tools", serverID), server.AllowedTools); err != nil {
				return err
			}
		}
	}
	if strings.TrimSpace(c.Server.ListenAddr) == "" {
		return errors.New("server.listen_addr 不能为空")
	}
	if c.Server.ReadTimeout <= 0 || c.Server.WriteTimeout <= 0 || c.Server.IdleTimeout <= 0 {
		return errors.New("server 超时必须大于 0")
	}
	if c.Server.RequestBodyLimit <= 0 || c.Server.SSEHeartbeat <= 0 {
		return errors.New("server 请求体上限和 SSE 心跳必须大于 0")
	}
	if c.Model.Provider == "" || c.Model.Name == "" || c.Model.Timeout <= 0 || c.Model.MaxTokens <= 0 {
		return errors.New("model 配置不完整")
	}
	switch c.Model.Provider {
	case "fake":
	case "deepseek":
		if strings.TrimSpace(c.Model.APIKeyEnv) == "" {
			return errors.New("deepseek 模型必须配置 model.api_key_env")
		}
	default:
		return fmt.Errorf("model provider %q 未注册", c.Model.Provider)
	}
	if c.Agent.Supervisor.Enabled {
		if c.Agent.Supervisor.Implementation != "eino.chat_model_agent" || c.Agent.Supervisor.MaxSteps <= 0 {
			return errors.New("Supervisor 实现未注册或 max_steps 非法")
		}
		if err := rejectDuplicateIDs("Supervisor allowed_workers", c.Agent.Supervisor.AllowedWorkers); err != nil {
			return err
		}
		for _, workerID := range c.Agent.Supervisor.AllowedWorkers {
			worker, ok := c.Agent.Workers[workerID]
			if !ok || !worker.Enabled {
				return fmt.Errorf("Supervisor 引用了未启用 Worker %q", workerID)
			}
		}
	}
	for workerID, worker := range c.Agent.Workers {
		if worker.Enabled && worker.Timeout <= 0 {
			return fmt.Errorf("Worker %q timeout 必须大于 0", workerID)
		}
		if err := rejectDuplicateIDs(fmt.Sprintf("Worker %q allowed_tools", workerID), worker.AllowedTools); err != nil {
			return err
		}
		for _, toolID := range worker.AllowedTools {
			tool, ok := c.Tools[toolID]
			if !ok || !tool.Enabled {
				return fmt.Errorf("Worker %q 引用了未启用 Tool %q", workerID, toolID)
			}
		}
	}
	for routeID, route := range c.Agent.Routes {
		if strings.TrimSpace(route.WorkerID) == "" || strings.TrimSpace(route.ToolID) == "" || strings.TrimSpace(route.Intent) == "" {
			return fmt.Errorf("route %q 缺少 worker/tool/intent", routeID)
		}
		worker, ok := c.Agent.Workers[route.WorkerID]
		if !ok || !worker.Enabled {
			return fmt.Errorf("route %q 引用了未启用 Worker %q", routeID, route.WorkerID)
		}
		if !contains(c.Agent.Supervisor.AllowedWorkers, route.WorkerID) {
			return fmt.Errorf("route %q 的 Worker %q 不在 Supervisor allow-list", routeID, route.WorkerID)
		}
		tool, ok := c.Tools[route.ToolID]
		if !ok || !tool.Enabled {
			return fmt.Errorf("route %q 引用了未启用 Tool %q", routeID, route.ToolID)
		}
		if !contains(worker.AllowedTools, route.ToolID) {
			return fmt.Errorf("route %q 的 Tool %q 不在 Worker allow-list", routeID, route.ToolID)
		}
		if route.Risk != "read_only" && route.Risk != "side_effect" {
			return fmt.Errorf("route %q risk 非法", routeID)
		}
		if tool.Risk == "side_effect" && route.Risk != "side_effect" {
			return fmt.Errorf("route %q 未声明副作用风险", routeID)
		}
		if len(route.Matches) == 0 {
			return fmt.Errorf("route %q 至少需要一个 matches", routeID)
		}
	}
	for toolID, tool := range c.Tools {
		if tool.FakeDelayMS < 0 {
			return fmt.Errorf("Tool %q fake_delay_ms 不能为负", toolID)
		}
		if tool.Enabled && (tool.Implementation == "gorm.mysql_order_query" || tool.Implementation == "gorm.mysql_order_insert") && !c.MySQL.Enabled {
			return fmt.Errorf("Tool %q 使用 MySQL 但 mysql.enabled 未开启", toolID)
		}
		if tool.Enabled && tool.Implementation == "http.read_only" {
			parsed, err := url.Parse(strings.TrimSpace(tool.Endpoint))
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return fmt.Errorf("Tool %q endpoint 必须是 http/https 地址", toolID)
			}
		}
		if tool.Enabled && tool.Implementation == "mcp.read_only" {
			if !c.MCP.Enabled || strings.TrimSpace(tool.MCPServer) == "" {
				return fmt.Errorf("Tool %q 使用 MCP 但未配置 mcp.enabled/mcp_server", toolID)
			}
			server, ok := c.MCP.Servers[tool.MCPServer]
			if !ok || !contains(server.AllowedTools, toolID) {
				return fmt.Errorf("Tool %q 不在 MCP server %q allow-list", toolID, tool.MCPServer)
			}
		}
		if tool.Enabled && tool.Timeout <= 0 {
			return fmt.Errorf("Tool %q timeout 必须大于 0", toolID)
		}
		if tool.Risk == "side_effect" && !tool.RequiresApproval {
			return fmt.Errorf("副作用 Tool %q 必须要求审批", toolID)
		}
	}
	if c.Limits.MaxPlanSteps <= 0 || c.Limits.MaxToolCalls <= 0 || c.Limits.MaxRetries < 0 || c.Limits.CostBudget <= 0 {
		return errors.New("limits 配置不合法")
	}
	if c.Approval.Timeout <= 0 || c.Approval.IdempotencyWindow <= 0 || c.Checkpoint.Retention <= 0 {
		return errors.New("approval/checkpoint 时间配置不合法")
	}
	if c.MySQL.Enabled && !validEnvName(c.MySQL.DSNEnv) {
		return errors.New("mysql.dsn_env 不是合法环境变量名")
	}
	if c.Observability.TraceSampleRate < 0 || c.Observability.TraceSampleRate > 1 {
		return errors.New("observability.trace_sample_rate 必须在 0 到 1 之间")
	}
	if c.Observability.Enabled && strings.TrimSpace(c.Observability.ServiceName) == "" {
		return errors.New("observability.service_name 不能为空")
	}
	if err := validateHTTPURL("observability.endpoint", c.Observability.Endpoint); err != nil {
		return err
	}
	if c.Observability.LogLevel != "" {
		switch strings.ToLower(strings.TrimSpace(c.Observability.LogLevel)) {
		case "debug", "info", "warn", "warning", "error":
		default:
			return fmt.Errorf("observability.log_level %q 未注册", c.Observability.LogLevel)
		}
	}
	if c.Observability.FileMode != "" && c.Observability.FileMode != "0600" {
		return errors.New("observability.file_mode 只允许 0600")
	}
	if c.Observability.RotateMaxBytes < 0 || c.Observability.RetentionFiles < 0 {
		return errors.New("observability 轮转和保留配置不能为负数")
	}
	if c.Observability.LangfuseEnabled {
		if strings.TrimSpace(c.Observability.LangfuseEndpoint) == "" {
			return errors.New("observability.langfuse_endpoint 不能为空")
		}
		if err := validateHTTPURL("observability.langfuse_endpoint", c.Observability.LangfuseEndpoint); err != nil {
			return err
		}
		if !validEnvName(c.Observability.LangfuseHeadersEnv) {
			return errors.New("observability.langfuse_headers_env 不是合法环境变量名")
		}
	}
	return nil
}

func validateHTTPURL(name, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s 必须是 http/https 地址", name)
	}
	return nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for index, r := range name {
		valid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_' || (index > 0 && r >= '0' && r <= '9')
		if !valid {
			return false
		}
	}
	return true
}

// applyEnvironment 只覆盖文档约定的部署字段，避免环境变量改变安全注册表。
func (c *Config) applyEnvironment(forceFake bool) error {
	if value := os.Getenv("LISTEN_ADDR"); value != "" {
		c.Server.ListenAddr = value
	}
	if value := os.Getenv("LOG_LEVEL"); value != "" {
		c.Observability.LogLevel = value
	}
	modelOverride := os.Getenv("MODEL_PROVIDER")
	if modelOverride != "" {
		c.Model.Provider = modelOverride
	}
	modelNameOverride := os.Getenv("LLM_MODEL")
	if modelNameOverride != "" {
		c.Model.Name = modelNameOverride
	}
	if modelOverride == "fake" && modelNameOverride == "" {
		c.Model.Name = "fake-model"
	}
	if value := os.Getenv("LLM_BASE_URL"); value != "" {
		c.Model.BaseURL = value
	}
	if value := os.Getenv("READ_ONLY_TOOL_ENDPOINT"); value != "" {
		tool := c.Tools["user_query"]
		tool.Endpoint = value
		c.Tools["user_query"] = tool
	}
	if value := os.Getenv("PERSISTENCE_BACKEND"); value != "" {
		c.Storage.Backend = value
	}
	if value := os.Getenv("PERSISTENCE_DIR"); value != "" {
		c.Storage.Dir = value
	}
	if value := os.Getenv("MYSQL_DSN_ENV"); value != "" {
		c.MySQL.DSNEnv = value
	}
	if value := os.Getenv("MYSQL_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("MYSQL_ENABLED 不是布尔值: %w", err)
		}
		c.MySQL.Enabled = enabled
	}
	if value := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); value != "" {
		c.Observability.Endpoint = value
	}
	if value := os.Getenv("OTEL_SERVICE_NAME"); value != "" {
		c.Observability.ServiceName = value
	}
	if value := os.Getenv("LANGFUSE_ENDPOINT"); value != "" {
		c.Observability.LangfuseEndpoint = value
	}
	if value := os.Getenv("LANGFUSE_HEADERS_ENV"); value != "" {
		c.Observability.LangfuseHeadersEnv = value
	}
	if value := os.Getenv("LANGFUSE_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("LANGFUSE_ENABLED 不是布尔值: %w", err)
		}
		c.Observability.LangfuseEnabled = enabled
	}
	if value := os.Getenv("OTEL_ENABLED"); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("OTEL_ENABLED 不是布尔值: %w", err)
		}
		c.Observability.Enabled = enabled
	}
	if value := os.Getenv("OTEL_EXPORTER_OTLP_INSECURE"); value != "" {
		insecure, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("OTEL_EXPORTER_OTLP_INSECURE 不是布尔值: %w", err)
		}
		c.Observability.Insecure = insecure
	}
	if value := os.Getenv("FAKE_MODEL"); value != "" {
		if enabled, err := strconv.ParseBool(value); err == nil && enabled {
			c.Model.Provider = "fake"
			c.Model.Name = "fake-model"
		} else if err != nil && !forceFake {
			return fmt.Errorf("FAKE_MODEL 不是布尔值: %w", err)
		}
	}
	return nil
}

// contains 判断固定工具 allow-list 是否包含目标值。
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func rejectDuplicateIDs(name string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s 包含重复引用 %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

// parseDurations 将 TOML 中的可读时间字符串转换成运行时类型。
func (c *Config) parseDurations() error {
	var err error
	parse := func(name, value string, target *time.Duration) error {
		if value == "" {
			return fmt.Errorf("%s 不能为空", name)
		}
		parsed, parseErr := time.ParseDuration(value)
		if parseErr != nil {
			return fmt.Errorf("%s 无效: %w", name, parseErr)
		}
		*target = parsed
		return nil
	}
	for _, item := range []struct {
		name, value string
		target      *time.Duration
	}{
		{"server.read_timeout", c.Server.ReadTimeoutText, &c.Server.ReadTimeout},
		{"server.write_timeout", c.Server.WriteTimeoutText, &c.Server.WriteTimeout},
		{"server.idle_timeout", c.Server.IdleTimeoutText, &c.Server.IdleTimeout},
		{"server.sse_heartbeat", c.Server.SSEHeartbeatText, &c.Server.SSEHeartbeat},
		{"model.timeout", c.Model.TimeoutText, &c.Model.Timeout},
		{"approval.timeout", c.Approval.TimeoutText, &c.Approval.Timeout},
		{"approval.idempotency_window", c.Approval.IdempotencyText, &c.Approval.IdempotencyWindow},
		{"checkpoint.retention", c.Checkpoint.RetentionText, &c.Checkpoint.Retention},
	} {
		if err = parse(item.name, item.value, item.target); err != nil {
			return err
		}
	}
	for workerID, worker := range c.Agent.Workers {
		if err = parse("agent.workers."+workerID+".timeout", worker.TimeoutText, &worker.Timeout); err != nil {
			return err
		}
		c.Agent.Workers[workerID] = worker
	}
	for toolID, tool := range c.Tools {
		if err = parse("tools."+toolID+".timeout", tool.TimeoutText, &tool.Timeout); err != nil {
			return err
		}
		c.Tools[toolID] = tool
	}
	return nil
}
