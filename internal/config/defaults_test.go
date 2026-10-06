package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// minimalConfigTOML 只声明无法默认的安全选择（模型、身份、Worker/Tool/路由注册），
// 所有运行参数都省略，用来验证"省略即用默认值"。
const minimalConfigTOML = `
[model]
provider = "fake"
name = "fake-model"

[auth]
implementation = "static_bearer"

[[auth.credentials]]
token_sha256_env = "AGENT_AUTH_DEMO_TOKEN_SHA256"
tenant_id = "demo-tenant"
subject_id = "demo-user"

[agent.supervisor]
enabled = true
implementation = "eino.chat_model_agent"
prompt_file = "supervisor.md"

[agent.workers.worker_a]
enabled = true
implementation = "fake.worker_a"
runner = "single_tool"
prompt_file = "worker_a.md"
allowed_tools = ["tool_a"]

[agent.routes.tool_a]
worker = "worker_a"
intent = "worker_a"
matches = ["查询"]
tool = "tool_a"
risk = "read_only"

[tools.tool_a]
enabled = true
implementation = "fake.tool_a"
risk = "read_only"
`

// TestMinimalConfigGetsEveryDefault 证明每个运行参数都有默认值：最小配置只声明安全
// 选择，其余字段全部由 applyDefaults 填充，代码里不再有等效硬编码。
func TestMinimalConfigGetsEveryDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "minimal.toml")
	if err := os.WriteFile(path, []byte(minimalConfigTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("minimal configuration must load: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"server.listen_addr", cfg.Server.ListenAddr, defaultListenAddr},
		{"server.read_timeout", cfg.Server.ReadTimeout, 10 * time.Second},
		{"server.write_timeout", cfg.Server.WriteTimeout, 30 * time.Second},
		{"server.idle_timeout", cfg.Server.IdleTimeout, 60 * time.Second},
		{"server.request_body_limit", cfg.Server.RequestBodyLimit, defaultRequestBodyLimit},
		{"server.sse_heartbeat", cfg.Server.SSEHeartbeat, defaultSSEHeartbeat},
		{"model.timeout", cfg.Model.Timeout, 30 * time.Second},
		{"model.max_tokens", cfg.Model.MaxTokens, 1024},
		{"agent.supervisor.max_steps", cfg.Agent.Supervisor.MaxSteps, defaultSupervisorMaxSteps},
		{"limits.max_plan_steps", cfg.Limits.MaxPlanSteps, 8},
		{"limits.max_tool_calls", cfg.Limits.MaxToolCalls, 12},
		{"limits.cost_budget", cfg.Limits.CostBudget, 12},
		{"limits.max_retries", cfg.Limits.MaxRetries, 0},
		{"approval.timeout", cfg.Approval.Timeout, 10 * time.Minute},
		{"checkpoint.retention", cfg.Checkpoint.Retention, 24 * time.Hour},
		{"observability.file_mode", cfg.Observability.FileMode, defaultFileMode},
		{"observability.file_permissions", cfg.Observability.FilePermissions, os.FileMode(0o600)},
		{"observability.retention_files", cfg.Observability.RetentionFiles, 5},
		{"observability.log_level", cfg.Observability.LogLevel, "info"},
		{"agent.workers.worker_a.timeout", cfg.Agent.Workers["worker_a"].Timeout, 20 * time.Second},
		{"tools.tool_a.timeout", cfg.Tools["tool_a"].Timeout, 5 * time.Second},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
}

// TestPromptPathsResolveAgainstConfigDir 证明提示词不依赖启动时的工作目录。
func TestPromptPathsResolveAgainstConfigDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "minimal.toml")
	if err := os.WriteFile(path, []byte(minimalConfigTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// 配置值保持作者书写的样子，注册快照才能按原样比对。
	if cfg.Agent.Supervisor.PromptFile != "supervisor.md" {
		t.Fatalf("config value should stay as authored, got %q", cfg.Agent.Supervisor.PromptFile)
	}
	if got := cfg.ResolvePath(cfg.Agent.Supervisor.PromptFile); got != filepath.Join(dir, "supervisor.md") {
		t.Fatalf("resolved prompt path = %q, want it under the config directory", got)
	}
	if got := cfg.ResolvePath("/abs/prompt.md"); got != "/abs/prompt.md" {
		t.Fatalf("absolute paths must be preserved, got %q", got)
	}
}

// TestFileModeRejectsLoosening 证明 file_mode 可配置但不能放宽到他人可写或全局可读。
func TestFileModeRejectsLoosening(t *testing.T) {
	for _, testCase := range []struct {
		value   string
		wantErr bool
	}{
		{value: "0600"},
		{value: "0640"},
		{value: "0o600"},
		{value: "0666", wantErr: true},
		{value: "0644", wantErr: true},
		{value: "not-octal", wantErr: true},
	} {
		t.Run(testCase.value, func(t *testing.T) {
			_, err := parseFileMode(testCase.value)
			if testCase.wantErr && err == nil {
				t.Fatalf("mode %q must be rejected", testCase.value)
			}
			if !testCase.wantErr && err != nil {
				t.Fatalf("mode %q must be accepted: %v", testCase.value, err)
			}
		})
	}
}
