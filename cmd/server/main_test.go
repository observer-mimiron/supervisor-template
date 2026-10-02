package main

import (
	"path/filepath"
	"testing"

	"github.com/observer-mimiron/supervisor-template/internal/config"
)

func TestForceFakeModelOverridesEnvironmentResolvedModel(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("LLM_BASE_URL", "")
	t.Setenv("FAKE_MODEL", "")
	cfg, err := config.Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model.Provider = "deepseek"
	cfg.Model.Name = "deepseek-chat"
	forceFakeModel(&cfg)
	if cfg.Model.Provider != "fake" || cfg.Model.Name != "fake-model" {
		t.Fatalf("fake flag did not win: %#v", cfg.Model)
	}
}
