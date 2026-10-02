package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSyntheticDataset(t *testing.T) {
	dataset, err := Load(filepath.Join("datasets", "synthetic-operations-v2.json"))
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

func TestLoadRejectsInvalidCaseContractFields(t *testing.T) {
	const validCase = `{"id":"case-1","version":"1","category":"positive","risk_level":"low","impact_tags":["query"],"business_goal":"query","preconditions":{"fixture":"synthetic_audience_v1","subject":"demo-user"},"request_steps":[{"action":"chat","message":"分析"}],"expected_results":{"terminal":"completed","event_types":["completed"]},"forbidden_side_effects":{"tool_ids":[],"max_writes":0},"evidence_requirements":["events","run_state","trace_correlation","cleanup","http_statuses","sequence"],"evaluator_rules":["business_correctness@1","architecture_boundary@1","side_effect_safety@1","stability@1"],"timeout":"1s","retry_budget":0,"idempotency_key":"case-1","cleanup_policy":"isolated_run"}`
	validDataset := `{"name":"x","version":"1","description":"x","cases":[` + validCase + `]}`
	dir := t.TempDir()
	path := filepath.Join(dir, "case-contract.json")
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{name: "valid baseline", data: validDataset},
		{name: "missing dataset version", data: strings.Replace(validDataset, `"version":"1"`, `"version":""`, 1), want: "dataset requires name, version and cases"},
		{name: "missing case version", data: strings.Replace(validDataset, `"id":"case-1","version":"1"`, `"id":"case-1","version":""`, 1), want: "duplicate or missing case id/version"},
		{name: "duplicate case id and version", data: `{"name":"x","version":"1","description":"x","cases":[` + validCase + `,` + validCase + `]}`, want: "duplicate or missing case id/version"},
		{name: "missing terminal expectation", data: strings.Replace(validDataset, `"terminal":"completed"`, `"terminal":""`, 1), want: "requires request steps and terminal expectation"},
		{name: "empty request steps", data: strings.Replace(validDataset, `"request_steps":[{"action":"chat","message":"分析"}]`, `"request_steps":[]`, 1), want: "requires request steps and terminal expectation"},
		{name: "zero timeout", data: strings.Replace(validDataset, `"timeout":"1s"`, `"timeout":"0s"`, 1), want: "timeout must be positive"},
		{name: "unparsable timeout", data: strings.Replace(validDataset, `"timeout":"1s"`, `"timeout":"soon"`, 1), want: "timeout must be positive"},
		{name: "missing timeout", data: strings.Replace(validDataset, `"timeout":"1s",`, ``, 1), want: "timeout is required"},
		{name: "negative retry budget", data: strings.Replace(validDataset, `"retry_budget":0`, `"retry_budget":-1`, 1), want: "invalid retry, idempotency or cleanup policy"},
		{name: "empty idempotency key", data: strings.Replace(validDataset, `"idempotency_key":"case-1"`, `"idempotency_key":""`, 1), want: "invalid retry, idempotency or cleanup policy"},
		{name: "unsupported cleanup policy", data: strings.Replace(validDataset, `"cleanup_policy":"isolated_run"`, `"cleanup_policy":"local-only"`, 1), want: "invalid retry, idempotency or cleanup policy"},
		{name: "unsupported category", data: strings.Replace(validDataset, `"category":"positive"`, `"category":"feature"`, 1), want: "invalid category, risk or fixture"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if test.want == "" {
				if err != nil {
					t.Fatalf("valid case rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadShippedFeatureAcceptanceTemplate(t *testing.T) {
	dataset, err := Load(filepath.Join("datasets", "feature-acceptance-template.json"))
	if err != nil {
		t.Fatalf("shipped feature-acceptance template does not load: %v", err)
	}
	if len(dataset.Cases) != 1 {
		t.Fatalf("template cases=%d, want 1", len(dataset.Cases))
	}
	item := dataset.Cases[0]
	if item.CleanupPolicy != "isolated_run" {
		t.Fatalf("template cleanup policy=%q, want isolated_run", item.CleanupPolicy)
	}
	declared := map[string]bool{}
	for _, requirement := range item.EvidenceRequirements {
		declared[requirement] = true
	}
	for _, requirement := range RequiredEvidence(item) {
		if !declared[requirement] {
			t.Fatalf("template does not declare required evidence %q", requirement)
		}
	}
}

func TestLoadPostconditionsRequireTheirEvidence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "postconditions.json")
	base := `{"name":"x","version":"1","description":"x","cases":[{"id":"case-1","version":"1","category":"positive","risk_level":"low","impact_tags":["query"],"business_goal":"query","preconditions":{"fixture":"synthetic_audience_v1","subject":"demo-user"},"request_steps":[{"action":"chat","message":"分析"}],"expected_results":{"terminal":"completed","event_types":["completed"]},"forbidden_side_effects":{"tool_ids":[],"max_writes":0},"evidence_requirements":["events","run_state","trace_correlation","cleanup","http_statuses","sequence","database_state","diagnostic_logs"],"evaluator_rules":["business_correctness@1","architecture_boundary@1","side_effect_safety@1","stability@1"],"postconditions":{"database":{"backend":"mysql","after_order_count":0},"logs":{"min_records":1}},"timeout":"1s","retry_budget":0,"idempotency_key":"case-1","cleanup_policy":"isolated_run"}]} `
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("valid postconditions rejected: %v", err)
	}
	missing := strings.Replace(base, `,"database_state","diagnostic_logs"`, ``, 1)
	if err := os.WriteFile(path, []byte(missing), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "database_state") {
		t.Fatalf("missing postcondition evidence error=%v", err)
	}
}

func TestLoadValidatesFixtureReferenceFormat(t *testing.T) {
	const validCase = `{"id":"case-1","version":"1","category":"positive","risk_level":"low","impact_tags":["query"],"business_goal":"query","preconditions":{"fixture":"synthetic_audience_v1","subject":"demo-user"},"request_steps":[{"action":"chat","message":"分析"}],"expected_results":{"terminal":"completed","event_types":["completed"]},"forbidden_side_effects":{"tool_ids":[],"max_writes":0},"evidence_requirements":["events","run_state","trace_correlation","cleanup","http_statuses","sequence"],"evaluator_rules":["business_correctness@1","architecture_boundary@1","side_effect_safety@1","stability@1"],"timeout":"1s","retry_budget":0,"idempotency_key":"case-1","cleanup_policy":"isolated_run"}`
	dataset := `{"name":"x","version":"1","description":"x","cases":[` + validCase + `]}`

	dir := t.TempDir()
	path := filepath.Join(dir, "fixture.json")
	write := func(t *testing.T, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 合法名字：通过。名字标识 Case 期望的环境，实际环境由 -config 决定。
	write(t, dataset)
	if _, err := Load(path); err != nil {
		t.Fatalf("valid fixture name rejected: %v", err)
	}
	write(t, strings.Replace(dataset, `"fixture":"synthetic_audience_v1"`, `"fixture":"another_env_v2"`, 1))
	if _, err := Load(path); err != nil {
		t.Fatalf("a syntactically valid fixture name was rejected: %v", err)
	}

	// 非法名字：没有任何环境能对应，属于 Case 合同错误。
	write(t, strings.Replace(dataset, `"fixture":"synthetic_audience_v1"`, `"fixture":"Ghost-Fixture"`, 1))
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "invalid fixture name") {
		t.Fatalf("invalid fixture name error=%v, want a format rejection", err)
	}
}
