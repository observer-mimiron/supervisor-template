package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExampleConfig(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("FAKE_MODEL", "")
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Supervisor.AllowedWorkers[0] != "user_analysis" {
		t.Fatalf("allowed workers = %#v", cfg.Agent.Supervisor.AllowedWorkers)
	}
	if _, ok := cfg.Agent.Routes["user_query"]; !ok {
		t.Fatalf("configured routes = %#v", cfg.Agent.Routes)
	}
	if cfg.Model.Provider != "deepseek" || cfg.Model.Name != "deepseek-chat" || cfg.Model.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("example model defaults = %#v", cfg.Model)
	}
}

func TestCompileCatalogCopiesConfigReferences(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := cfg.CompileCatalog()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.Workers["user_analysis"] = WorkerConfig{}
	cfg.Agent.Routes["user_query"] = RouteConfig{}
	if !catalog.Workers["user_analysis"].Enabled || catalog.Routes["user_query"].ToolID != "user_query" {
		t.Fatalf("catalog was not detached from config: %#v", catalog)
	}
}

func TestCompileRuntimeCatalogReturnsFrozenCopies(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := cfg.CompileRuntimeCatalog()
	if err != nil {
		t.Fatal(err)
	}
	workers := catalog.Workers()
	workers["user_analysis"] = WorkerConfig{}
	workers["user_analysis"] = catalog.Workers()["user_analysis"]
	tools := catalog.Tools()
	tool := tools["user_query"]
	tool.Implementation = "tampered"
	tools["user_query"] = tool
	if catalog.Workers()["user_analysis"].Implementation != "fake.user_analysis" || catalog.Tools()["user_query"].Implementation != "fake.user_query" {
		t.Fatal("runtime catalog leaked mutable map state")
	}
	supervisor := catalog.Supervisor()
	supervisor.AllowedWorkers[0] = "tampered"
	if catalog.Supervisor().AllowedWorkers[0] != "user_analysis" {
		t.Fatal("runtime catalog leaked mutable slice state")
	}
}

func TestCompileRuntimeCatalogRejectsMissingPromptAndRunner(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.Supervisor.PromptFile = ""
	if _, err := cfg.CompileRuntimeCatalog(); err == nil || !strings.Contains(err.Error(), "Supervisor Prompt") {
		t.Fatalf("expected missing supervisor prompt rejection, got %v", err)
	}
	cfg, _ = Load(filepath.Join("..", "..", "config.example.toml"))
	worker := cfg.Agent.Workers["user_analysis"]
	worker.Runner = ""
	cfg.Agent.Workers["user_analysis"] = worker
	if _, err := cfg.CompileRuntimeCatalog(); err == nil || !strings.Contains(err.Error(), "Runner") {
		t.Fatalf("expected missing worker runner rejection, got %v", err)
	}
}

func TestValidateRejectsDuplicateAllowListReferences(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.Workers["user_analysis"] = WorkerConfig{
		Enabled: true, Implementation: "fake.user_analysis", Runner: "single_tool",
		PromptFile: "prompts/user_analysis.md", AllowedTools: []string{"user_query", "user_query"}, Timeout: cfg.Agent.Workers["user_analysis"].Timeout,
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "重复引用") {
		t.Fatalf("expected duplicate allow-list rejection, got %v", err)
	}
}

func TestValidateRejectsRouteOutsideWorkerAllowList(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	route := cfg.Agent.Routes["user_query"]
	route.ToolID = "missing_tool"
	cfg.Agent.Routes["user_query"] = route
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "未启用 Tool") {
		t.Fatalf("expected route tool rejection, got %v", err)
	}
}

func TestValidateRejectsIncompleteAuthCredential(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Auth.Credentials[0].TokenSHA256Env = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "auth credential") {
		t.Fatalf("expected auth credential rejection, got %v", err)
	}
}

func TestValidateRejectsUnknownSupervisorWorker(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Agent.Supervisor.AllowedWorkers = []string{"missing_worker"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "未启用 Worker") {
		t.Fatalf("expected unknown worker rejection, got %v", err)
	}
}

func TestLoadAppliesOnlyDocumentedEnvironmentOverrides(t *testing.T) {
	t.Setenv("LISTEN_ADDR", ":18080")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("FAKE_MODEL", "true")
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ListenAddr != ":18080" || cfg.Observability.LogLevel != "debug" || cfg.Model.Provider != "fake" || cfg.Model.Name != "fake-model" {
		t.Fatalf("environment override not applied: %#v", cfg)
	}
	t.Setenv("FAKE_MODEL", "invalid")
	if _, err := Load(filepath.Join("..", "..", "config.example.toml")); err == nil {
		t.Fatal("expected invalid FAKE_MODEL to fail")
	}
}

func TestLoadModelProviderOverrideAndFakeSwitch(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "fake")
	t.Setenv("FAKE_MODEL", "")
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Provider != "fake" || cfg.Model.Name != "fake-model" {
		t.Fatalf("MODEL_PROVIDER did not override TOML: %#v", cfg.Model)
	}

	t.Setenv("MODEL_PROVIDER", "deepseek")
	t.Setenv("FAKE_MODEL", "true")
	cfg, err = Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Provider != "fake" || cfg.Model.Name != "fake-model" {
		t.Fatalf("FAKE_MODEL=true did not win after MODEL_PROVIDER: %q", cfg.Model.Provider)
	}
}

func TestLoadWithFakeWinsBeforeValidation(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "unregistered-provider")
	t.Setenv("LLM_MODEL", "unregistered-model")
	t.Setenv("FAKE_MODEL", "invalid")
	cfg, err := LoadWithFake(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model.Provider != "fake" || cfg.Model.Name != "fake-model" {
		t.Fatalf("forced fake did not win before validation: %#v", cfg.Model)
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	contents := "[model]\nprovider = \"fake\"\nname = \"fake-model\"\napi_key = \"should-not-be-here\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "model.api_key") {
		t.Fatalf("expected unknown field rejection, got %v", err)
	}
}

func TestValidateRecognizesM3ProvidersAndRejectsMissingReadOnlyEndpoint(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model.Provider = "deepseek"
	cfg.Model.APIKeyEnv = "TEST_LLM_API_KEY"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("deepseek provider should be registered: %v", err)
	}

	cfg.Model.Provider = "fake"
	cfg.Tools["user_query"] = ToolConfig{
		Enabled:        true,
		Implementation: "http.read_only",
		Risk:           "read_only",
		Timeout:        cfg.Tools["user_query"].Timeout,
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("expected missing endpoint rejection, got %v", err)
	}
	cfg.Tools["user_query"] = ToolConfig{
		Enabled:        true,
		Implementation: "http.read_only",
		Endpoint:       "https://example.test/lookup",
		Risk:           "read_only",
		Timeout:        5 * time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("configured read-only endpoint should validate: %v", err)
	}
}

func TestValidateObservabilityBoundaries(t *testing.T) {
	base, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{name: "endpoint scheme", edit: func(cfg *Config) { cfg.Observability.Endpoint = "ftp://collector" }, want: "http/https"},
		{name: "log level", edit: func(cfg *Config) { cfg.Observability.LogLevel = "trace" }, want: "log_level"},
		{name: "file mode", edit: func(cfg *Config) { cfg.Observability.FileMode = "0644" }, want: "file_mode"},
		{name: "negative rotation", edit: func(cfg *Config) { cfg.Observability.RotateMaxBytes = -1 }, want: "轮转"},
		{name: "langfuse endpoint required", edit: func(cfg *Config) { cfg.Observability.LangfuseEnabled = true }, want: "langfuse_endpoint"},
		{name: "langfuse endpoint scheme", edit: func(cfg *Config) {
			cfg.Observability.LangfuseEnabled = true
			cfg.Observability.LangfuseEndpoint = "collector"
		}, want: "langfuse_endpoint"},
		{name: "langfuse header env", edit: func(cfg *Config) {
			cfg.Observability.LangfuseEnabled = true
			cfg.Observability.LangfuseEndpoint = "https://langfuse.test"
			cfg.Observability.LangfuseHeadersEnv = "1HEADERS"
		}, want: "langfuse_headers_env"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.edit(&cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateObservabilityLangfuseConfig(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Observability.LangfuseEnabled = true
	cfg.Observability.LangfuseEndpoint = "https://langfuse.test/api/public/otel"
	cfg.Observability.LangfuseHeadersEnv = "LANGFUSE_AUTH"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid Langfuse config rejected: %v", err)
	}
}

func TestValidateMySQLOptInBoundaries(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MySQL.Enabled {
		t.Fatal("example config must keep MySQL disabled by default")
	}
	cfg.MySQL.DSNEnv = ""
	cfg.Tools["mysql_order_query"] = ToolConfig{
		Enabled: true, Implementation: "gorm.mysql_order_query", Risk: "read_only", Timeout: 5 * time.Second,
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mysql.enabled") {
		t.Fatalf("expected MySQL tool without adapter rejection, got %v", err)
	}

	cfg.MySQL.Enabled = true
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "mysql.dsn_env") {
		t.Fatalf("expected missing MySQL DSN environment name rejection, got %v", err)
	}

	cfg.MySQL.DSNEnv = "MYSQL_ORDER_DSN"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid MySQL opt-in should validate: %v", err)
	}
}
