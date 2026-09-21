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
