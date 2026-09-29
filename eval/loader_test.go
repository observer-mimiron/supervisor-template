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

func TestLoadRejectsUndeclaredEvidenceAndDuplicateRules(t *testing.T) {
	dir := t.TempDir()
	base := `{"name":"x","version":"1","description":"x","cases":[{"id":"case-1","version":"1","category":"positive","risk_level":"low","impact_tags":["query"],"business_goal":"query","preconditions":{"fixture":"synthetic_audience_v1","subject":"demo-user"},"request_steps":[{"action":"chat","message":"分析"}],"expected_results":{"terminal":"completed","event_types":["completed"]},"forbidden_side_effects":{"tool_ids":[],"max_writes":0},"evidence_requirements":["events","run_state","trace_correlation","cleanup","http_statuses","sequence"],"evaluator_rules":["business_correctness@1","architecture_boundary@1","side_effect_safety@1","stability@1"],"timeout":"1s","retry_budget":0,"idempotency_key":"case-1","cleanup_policy":"isolated_run"}]} `
	path := filepath.Join(dir, "case.json")
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{name: "unknown evidence", data: strings.Replace(base, `["events","run_state","trace_correlation","cleanup","http_statuses","sequence"]`, `["made_up"]`, 1), want: "evidence requirement"},
		{name: "duplicate rule", data: strings.Replace(base, `["business_correctness@1","architecture_boundary@1","side_effect_safety@1","stability@1"]`, `["business_correctness@1","business_correctness@1","architecture_boundary@1","side_effect_safety@1","stability@1"]`, 1), want: "unknown evaluator"},
		{name: "missing evaluator", data: strings.Replace(base, `["business_correctness@1","architecture_boundary@1","side_effect_safety@1","stability@1"]`, `["business_correctness@1","architecture_boundary@1","side_effect_safety@1"]`, 1), want: "must declare evaluator"},
		{name: "missing mandatory evidence", data: strings.Replace(base, `["events","run_state","trace_correlation","cleanup","http_statuses","sequence"]`, `["events","run_state","trace_correlation","cleanup","http_statuses"]`, 1), want: "must declare required evidence"},
		{name: "invalid terminal", data: strings.Replace(base, `"terminal":"completed"`, `"terminal":"unknown"`, 1), want: "terminal expectation"},
		{name: "invalid event type", data: strings.Replace(base, `"event_types":["completed"]`, `"event_types":["unknown"]`, 1), want: "invalid event type"},
		{name: "negative max writes", data: strings.Replace(base, `"max_writes":0`, `"max_writes":-1`, 1), want: "max_writes cannot be negative"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load error=%v, want %q", err, test.want)
			}
		})
	}
}
