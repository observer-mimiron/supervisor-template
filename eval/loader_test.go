package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSyntheticDataset(t *testing.T) {
	dataset, err := Load(filepath.Join("datasets", "synthetic-operations-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	categories := map[string]bool{}
	for _, item := range dataset.Cases {
		categories[item.Category] = true
	}
	if len(dataset.Cases) < 5 || !categories["diversity"] {
		t.Fatalf("cases=%d categories=%v, want diversity case", len(dataset.Cases), categories)
	}
}

func TestLoadRejectsDuplicateKeysAndUnknownFields(t *testing.T) {
	dir := t.TempDir()
	duplicate := `{"name":"x","name":"y","version":"1","description":"x","cases":[]}`
	path := filepath.Join(dir, "duplicate.json")
	if err := os.WriteFile(path, []byte(duplicate), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate keys error=%v", err)
	}
	unknown := `{"name":"x","version":"1","description":"x","extra":true,"cases":[]}`
	if err := os.WriteFile(path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error=%v", err)
	}
}

func TestLoadRejectsDynamicAndSecretLikeCaseText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unsafe.json")
	data := `{"name":"x","version":"1","description":"x","cases":[{"id":"case-1","version":"1","category":"positive","risk_level":"low","impact_tags":["query"],"business_goal":"${dynamic}","preconditions":{"fixture":"synthetic_audience_v1","subject":"demo-user"},"request_steps":[{"action":"chat","message":"分析"}],"expected_results":{"terminal":"completed","event_types":["completed"]},"forbidden_side_effects":{"tool_ids":[],"max_writes":0},"evidence_requirements":["events"],"evaluator_rules":["business_correctness@1"],"timeout":"1s","retry_budget":0,"idempotency_key":"case-1","cleanup_policy":"isolated_run"}]} `
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "dynamic") {
		t.Fatalf("unsafe case error=%v", err)
	}
}
