package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExampleConfig(t *testing.T) {
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
	if cfg.Server.ListenAddr != ":18080" || cfg.Observability.LogLevel != "debug" || cfg.Model.Provider != "fake" {
		t.Fatalf("environment override not applied: %#v", cfg)
	}
	t.Setenv("FAKE_MODEL", "invalid")
	if _, err := Load(filepath.Join("..", "..", "config.example.toml")); err == nil {
		t.Fatal("expected invalid FAKE_MODEL to fail")
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
