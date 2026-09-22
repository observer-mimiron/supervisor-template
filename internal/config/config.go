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

// ObservabilityConfig 定义可选的 OpenTelemetry 导出参数；认证头只从标准环境变量读取。
type ObservabilityConfig struct {
	Enabled         bool    `toml:"enabled"`
	Endpoint        string  `toml:"endpoint"`
	ServiceName     string  `toml:"service_name"`
	Insecure        bool    `toml:"insecure"`
	LogLevel        string  `toml:"log_level"`
	TraceSampleRate float64 `toml:"trace_sample_rate"`
	MetricsEnabled  bool    `toml:"metrics_enabled"`
}

// Load 从 TOML 文件加载配置，再应用环境变量覆盖并执行启动校验。
func Load(path string) (Config, error) {
	var cfg Config
	if path == "" {
		return cfg, errors.New("配置文件路径不能为空")
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return cfg, fmt.Errorf("读取配置文件失败: %w", err)
	}
	if err := cfg.applyEnvironment(); err != nil {
		return cfg, err
	}
	if err := cfg.parseDurations(); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
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
		for _, workerID := range c.Agent.Supervisor.AllowedWorkers {
			worker, ok := c.Agent.Workers[workerID]
			if !ok || !worker.Enabled {
				return fmt.Errorf("Supervisor 引用了未启用 Worker %q", workerID)
			}
		}
	}
	for workerID, worker := range c.Agent.Workers {
		if worker.Enabled && worker.Implementation != "fake.user_analysis" {
			return fmt.Errorf("Worker %q 实现未注册", workerID)
		}
		if worker.Enabled && worker.Runner != "" && worker.Runner != "single_tool" {
			return fmt.Errorf("Worker %q Runner %q 未注册", workerID, worker.Runner)
		}
		if worker.Enabled && worker.Timeout <= 0 {
			return fmt.Errorf("Worker %q timeout 必须大于 0", workerID)
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
		if tool.Enabled && tool.Implementation != "fake.user_query" && tool.Implementation != "fake.simulated_outreach" && tool.Implementation != "http.read_only" && tool.Implementation != "mcp.read_only" {
			return fmt.Errorf("Tool %q 实现未注册", toolID)
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
	if c.Observability.TraceSampleRate < 0 || c.Observability.TraceSampleRate > 1 {
		return errors.New("observability.trace_sample_rate 必须在 0 到 1 之间")
	}
	if c.Observability.Enabled && c.Observability.ServiceName == "" {
		return errors.New("observability.service_name 不能为空")
	}
	return nil
}

// applyEnvironment 只覆盖文档约定的部署字段，避免环境变量改变安全注册表。
func (c *Config) applyEnvironment() error {
	if value := os.Getenv("LISTEN_ADDR"); value != "" {
		c.Server.ListenAddr = value
	}
	if value := os.Getenv("LOG_LEVEL"); value != "" {
		c.Observability.LogLevel = value
	}
	if value := os.Getenv("MODEL_PROVIDER"); value != "" {
		c.Model.Provider = value
	}
	if value := os.Getenv("LLM_MODEL"); value != "" {
		c.Model.Name = value
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
	if value := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); value != "" {
		c.Observability.Endpoint = value
	}
	if value := os.Getenv("OTEL_SERVICE_NAME"); value != "" {
		c.Observability.ServiceName = value
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
		} else if err != nil {
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
