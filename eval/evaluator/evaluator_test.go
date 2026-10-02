package evaluator

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	eval "github.com/observer-mimiron/supervisor-template/eval"
	"github.com/observer-mimiron/supervisor-template/eval/runner"
	"github.com/observer-mimiron/supervisor-template/internal/composition"
)

func allEvaluatorRules() []string {
	return []string{"business_correctness@1", "architecture_boundary@1", "side_effect_safety@1", "stability@1"}
}

func resultFor(report CaseReport, evaluator string) (Result, bool) {
	for _, result := range report.Results {
		if result.Evaluator == evaluator {
			return result, true
		}
	}
	return Result{}, false
}

func TestEvaluateReadOnlyEvidence(t *testing.T) {
	item := eval.Case{ID: "case-1", Version: "1", EvaluatorRules: allEvaluatorRules(), ExpectedResults: eval.ExpectedResults{Terminal: "completed", EventTypes: []string{"started", "completed"}}, ForbiddenEffects: eval.ForbiddenEffects{MaxWrites: 0}}
	evidence := runner.Evidence{CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, TraceID: "trace", Terminal: "completed", CleanupResult: "ok", HTTPStatus: []int{200}, Events: []runner.Event{{EventID: "evt-1", RunID: item.ID, TraceID: "trace", Sequence: 1, Type: "started"}, {EventID: "evt-2", RunID: item.ID, TraceID: "trace", Sequence: 2, Type: "completed"}}}
	result := Evaluate(item, evidence)
	if !result.Passed || len(result.Results) != 4 {
		t.Fatalf("unexpected report: %#v", result)
	}
}

func TestBusinessAssertsStructuredExpectedResult(t *testing.T) {
	item := eval.Case{ID: "case-result", Version: "1", ExpectedResults: eval.ExpectedResults{
		Terminal: "completed", EventTypes: []string{"text", "completed"},
		Result: map[string]any{"count": 4, "customer_ids": []any{"cust-001"}, "spend_365d_total": 6200},
	}}
	evidence := runner.Evidence{CaseID: item.ID, CaseVersion: item.Version, RunID: "run-result", Terminal: "completed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, Events: []runner.Event{{EventID: "evt-1", RunID: "run-result", TraceID: "trace", Sequence: 1, Type: "text"}, {EventID: "evt-2", RunID: "run-result", TraceID: "trace", Sequence: 2, Type: "completed"}}, Result: map[string]any{"count": 4, "customer_ids": []any{"cust-001"}, "spend_365d_total": 6200}}
	if result, ok := resultFor(Evaluate(item, evidence), "business_correctness"); !ok || !result.Passed {
		t.Fatalf("matching structured result failed: %#v", result)
	}
	evidence.Result["count"] = 3
	result, ok := resultFor(Evaluate(item, evidence), "business_correctness")
	if !ok || result.Passed || result.FailedAssertion != "result" {
		t.Fatalf("mismatched structured result=%#v", result)
	}
}

func TestDatabaseAndLogPostconditionsAreDeterministic(t *testing.T) {
	item := eval.Case{
		ID: "case-postconditions", Version: "1", EvaluatorRules: allEvaluatorRules(),
		ExpectedResults: eval.ExpectedResults{Terminal: "completed", EventTypes: []string{"completed"}},
		Postconditions: eval.Postconditions{
			Database: &eval.DatabasePostcondition{Backend: "mysql", BeforeOrderCount: intPtr(0), AfterOrderCount: intPtr(1), ExpectedOrders: []eval.DatabaseOrderExpectation{{UserID: 1, ProductID: 1, Quantity: 2, TotalAmount: "39.80"}}},
			Logs:     &eval.LogPostcondition{MinRecords: 1, RequiredPhases: []string{"tool"}},
		},
	}
	evidence := runner.Evidence{
		CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Terminal: "completed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200},
		Events:   []runner.Event{{EventID: "evt-1", RunID: item.ID, TraceID: "trace", Sequence: 1, Type: "completed"}},
		Database: runner.DatabaseEvidence{Available: true, Backend: "mysql", BeforeOrderCount: 0, AfterOrderCount: 1, AfterOrderDigest: expectedOrderDigest(item.Postconditions.Database.ExpectedOrders)},
		Logs:     runner.DiagnosticLogEvidence{Available: true, RecordCount: 2, Phases: []string{"tool"}, RedactionPassed: true},
	}
	report := Evaluate(item, evidence)
	if !report.Passed {
		t.Fatalf("postcondition report failed: %#v", report)
	}
	evidence.Database.AfterOrderDigest = "wrong"
	result, ok := resultFor(Evaluate(item, evidence), "database_postcondition")
	if !ok || result.Passed || result.FailedAssertion != "after_order_state" {
		t.Fatalf("database mismatch=%#v", result)
	}
}

func intPtr(value int) *int { return &value }

func TestStabilityChecksRetryBudgetAndStepReference(t *testing.T) {
	item := eval.Case{ID: "case-stability", Version: "1", RetryBudget: 1}
	evidence := runner.Evidence{CaseID: item.ID, CaseVersion: item.Version, RunID: "run-1", Error: "failed", Terminal: "failed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, Retries: 2, FailedStepID: "run-1:step-1"}
	report := Evaluate(item, evidence)
	result, ok := resultFor(report, "stability")
	if !ok || result.Passed || result.FailedAssertion != "retry_budget" || result.EvidenceRef != "case-stability#run-1:step-1" {
		t.Fatalf("retry stability=%#v", result)
	}
	evidence.Retries = 0
	evidence.RepeatChecked = true
	evidence.RepeatVerdictStable = false
	result, ok = resultFor(Evaluate(item, evidence), "stability")
	if !ok || result.Passed || result.FailedAssertion != "repeat_verdict" {
		t.Fatalf("repeat stability=%#v", result)
	}
}

func TestExpectedRequestErrorHasNoFailureAssertion(t *testing.T) {
	item := eval.Case{ID: "case-request-error", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "request_error"}}
	evidence := runner.Evidence{CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Error: "HTTP 400", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{400}}
	result, ok := resultFor(Evaluate(item, evidence), "stability")
	if !ok || !result.Passed || result.FailedAssertion != "" || result.FailureTaxonomy != "" {
		t.Fatalf("expected request error stability=%#v", result)
	}
}

func TestArchitectureUsesRuntimeSnapshotAndPolicyEvidence(t *testing.T) {
	item := eval.Case{ID: "case-architecture", Version: "1"}
	evidence := runner.Evidence{
		CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, RegisteredTools: []string{"registered"},
		WorkerToolAllowList: map[string][]string{"worker": {"registered"}},
		Events: []runner.Event{
			{EventID: "evt-1", RunID: item.ID, Sequence: 1, Type: "decision", Data: map[string]string{"worker_id": "worker", "tool_id": "registered"}},
			{EventID: "evt-2", RunID: item.ID, Sequence: 2, Type: "tool_call", Data: map[string]string{"worker_id": "worker", "tool_id": "registered"}},
		},
	}
	result, ok := resultFor(Evaluate(item, evidence), "architecture_boundary")
	if !ok || !result.Passed || result.FailedAssertion != "" {
		t.Fatalf("runtime snapshot architecture result=%#v", result)
	}
	evidence.RegisteredTools = []string{"different"}
	result, ok = resultFor(Evaluate(item, evidence), "architecture_boundary")
	if !ok || result.Passed || result.FailedAssertion != "registered_tool" || result.FailureTaxonomy != "architecture" {
		t.Fatalf("dynamic allow-list failure=%#v", result)
	}
}

func TestFailedAssertionsBecomeFailureRecords(t *testing.T) {
	item := eval.Case{ID: "case-failure", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "completed", EventTypes: []string{"completed"}}}
	report := Evaluate(item, runner.Evidence{CaseID: item.ID, RunID: "run-1"})
	if report.Passed || len(report.Failures) == 0 || report.Failures[0].FailedAssertion == "" {
		t.Fatalf("failure records=%#v", report.Failures)
	}
}

func TestWriteJSONLIncludesEnvelopeAndCaseLines(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/report.jsonl"
	jsonPath := dir + "/report.json"
	report := Report{ReportSchemaVersion: ReportSchemaVersion, DatasetName: "dataset", DatasetVersion: "1", CodeVersion: "dev", EvaluationProfile: eval.RuntimeFakeProfile(eval.TierPR), Cases: []CaseReport{{CaseID: "case-1", CaseVersion: "1"}}}
	if err := WriteJSONL(path, report); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(jsonPath, report); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	lines := 0
	var envelope map[string]any
	for scanner.Scan() {
		lines++
		if lines != 1 {
			continue
		}
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lines != 2 {
		t.Fatalf("JSONL lines = %d, want 2", lines)
	}

	// Both report formats must carry the same explicit profile so a line or
	// file is never ambiguous when read alone.
	profile, ok := envelope["evaluation_profile"].(map[string]any)
	if !ok {
		t.Fatalf("JSONL envelope has no evaluation_profile: %v", envelope)
	}
	if profile["scenario"] != "runtime-fake" || profile["evaluation_plane"] != "runtime" || profile["executor"] != "local-fake" || profile["model_provider"] != "fake" {
		t.Fatalf("JSONL profile=%v", profile)
	}
	decoded, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	var jsonReport map[string]any
	if err := json.Unmarshal(decoded, &jsonReport); err != nil {
		t.Fatal(err)
	}
	jsonProfile, ok := jsonReport["evaluation_profile"].(map[string]any)
	if !ok || jsonProfile["scenario"] != "runtime-fake" {
		t.Fatalf("JSON report has no runtime-fake profile: %v", jsonReport)
	}

	// Both formats must declare the same schema version so a consumer can
	// refuse a report it does not understand instead of misreading it.
	if got, want := envelope["report_schema_version"], float64(ReportSchemaVersion); got != want {
		t.Fatalf("JSONL envelope report_schema_version=%v, want %v", got, want)
	}
	if got, want := jsonReport["report_schema_version"], float64(ReportSchemaVersion); got != want {
		t.Fatalf("JSON report report_schema_version=%v, want %v", got, want)
	}
	if envelope["report_schema_version"] != jsonReport["report_schema_version"] {
		t.Fatalf("JSON and JSONL disagree on report_schema_version: %v vs %v", jsonReport["report_schema_version"], envelope["report_schema_version"])
	}

	// Neither format may carry credentials, prompts, tool payloads, SQL values
	// or host absolute paths.
	jsonl, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"json": string(decoded), "jsonl": string(jsonl)} {
		lowered := strings.ToLower(content)
		for _, marker := range []string{"api_key", "authorization", "bearer ", "password", "-----begin", "/home/", "/workspace/", "/etc/"} {
			if strings.Contains(lowered, marker) {
				t.Fatalf("%s report contains sensitive marker %q", name, marker)
			}
		}
	}
}

func TestBuildWithProfileRejectsMismatchedCaseProfile(t *testing.T) {
	item := eval.Case{ID: "profile-case", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "request_error"}}
	dataset := eval.Dataset{Name: "dataset", Version: "1", Cases: []eval.Case{item}}
	profile := eval.RuntimeFakeProfile(eval.TierPR)
	evidence := runner.Evidence{CaseID: item.ID, CaseVersion: item.Version, RunID: "run-1", Profile: eval.RuntimeFakeProfile(eval.TierNightly), Error: "HTTP 400", HTTPStatus: []int{400}, CleanupResult: "ok"}
	report := BuildWithProfile(dataset, "dev", eval.Selection{}, profile, []CaseReport{{CaseID: item.ID, CaseVersion: item.Version, Evidence: evidence}})
	if report.Passed != 0 || report.Failed != 1 || len(report.Cases) != 1 {
		t.Fatalf("profile mismatch passed: %#v", report)
	}
	result, ok := resultFor(report.Cases[0], "evaluation_setup")
	if !ok || result.FailedAssertion != "evaluation_setup_error" {
		t.Fatalf("profile mismatch did not fail setup: %#v", report.Cases[0])
	}
}

func TestEvaluatorRulesRequireAllHardDimensions(t *testing.T) {
	item := eval.Case{ID: "case-rules", Version: "1", EvaluatorRules: []string{"stability@1"}, ExpectedResults: eval.ExpectedResults{Terminal: "request_error"}}
	evidence := runner.Evidence{CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Error: "HTTP 400", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{400}}
	report := Evaluate(item, evidence)
	stability, stabilityOK := resultFor(report, "stability")
	contract, contractOK := resultFor(report, "evaluator_contract")
	if !stabilityOK || !stability.Passed || !contractOK || contract.Passed || !strings.Contains(contract.Reason, "missing:business_correctness@1") {
		t.Fatalf("fixed evaluator contract=%#v", report)
	}
}

func TestMissingEvidenceCannotPassUnrelatedEvaluator(t *testing.T) {
	item := eval.Case{ID: "case-evidence", Version: "1", EvidenceRequirements: []string{"tool_calls"}, EvaluatorRules: []string{"stability@1"}, ExpectedResults: eval.ExpectedResults{Terminal: "request_error"}}
	evidence := runner.Evidence{CaseID: item.ID, Error: "HTTP 400", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{400}}
	report := Evaluate(item, evidence)
	result, ok := resultFor(report, "evidence_contract")
	if report.Passed || !ok || result.FailedAssertion != "missing_evidence" {
		t.Fatalf("missing evidence=%#v", report)
	}
}

func TestSideEffectSafetyUsesToolMetadataInsteadOfToolID(t *testing.T) {
	item := eval.Case{ID: "case-generic-side-effect", Version: "1", EvaluatorRules: []string{"side_effect_safety@1"}, ExpectedResults: eval.ExpectedResults{Terminal: "completed"}, ForbiddenEffects: eval.ForbiddenEffects{MaxWrites: 1}}
	base := runner.Evidence{
		CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Terminal: "completed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, FakeWriteCount: 1,
		RegisteredToolMetadata: map[string]composition.ToolMetadata{"custom_write": {Risk: "side_effect", RequiresApproval: true, IdempotencyRequired: true}},
		Events: []runner.Event{
			{EventID: "evt-1", RunID: item.ID, Sequence: 1, Type: "approval_required", Data: map[string]string{"step_id": "step-1", "worker_id": "worker", "tool_id": "custom_write"}},
			{EventID: "evt-2", RunID: item.ID, Sequence: 2, Type: "tool_call", Data: map[string]string{"step_id": "step-1", "worker_id": "worker", "tool_id": "custom_write"}},
			{EventID: "evt-3", RunID: item.ID, Sequence: 3, Type: "completed"},
		},
	}
	if result, ok := resultFor(Evaluate(item, base), "side_effect_safety"); !ok || !result.Passed {
		t.Fatalf("metadata side effect should pass: %#v", result)
	}
	base.Events = base.Events[1:]
	result, ok := resultFor(Evaluate(item, base), "side_effect_safety")
	if !ok || result.Passed || result.FailedAssertion != "approval_before_side_effect" {
		t.Fatalf("missing approval should fail: %#v", result)
	}
	base.RegisteredTools = []string{"custom_write"}
	base.RegisteredToolMetadata = nil
	result, ok = resultFor(Evaluate(item, base), "side_effect_safety")
	if !ok || result.Passed || result.FailedAssertion != "tool_contract" {
		t.Fatalf("missing tool metadata should fail: %#v", result)
	}
}

func TestSideEffectSafetyRejectsApprovalForAnotherTool(t *testing.T) {
	item := eval.Case{ID: "case-approval-binding", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "completed"}, ForbiddenEffects: eval.ForbiddenEffects{MaxWrites: 1}}
	evidence := runner.Evidence{
		CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Terminal: "completed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, FakeWriteCount: 1,
		RegisteredToolMetadata: map[string]composition.ToolMetadata{"custom_write": {Risk: "side_effect", RequiresApproval: true, IdempotencyRequired: true}},
		Events: []runner.Event{
			{EventID: "evt-1", RunID: item.ID, Sequence: 1, Type: "approval_required", Data: map[string]string{"step_id": "step-a", "worker_id": "worker", "tool_id": "other_write"}},
			{EventID: "evt-2", RunID: item.ID, Sequence: 2, Type: "tool_call", Data: map[string]string{"step_id": "step-b", "worker_id": "worker", "tool_id": "custom_write"}},
			{EventID: "evt-3", RunID: item.ID, Sequence: 3, Type: "completed"},
		},
	}
	result, ok := resultFor(Evaluate(item, evidence), "side_effect_safety")
	if !ok || result.Passed || result.FailedAssertion != "approval_before_side_effect" {
		t.Fatalf("approval for another tool passed: %#v", result)
	}
}

func TestSideEffectSafetyRequiresApprovalBindingWorkerAndTool(t *testing.T) {
	item := eval.Case{ID: "case-approval-binding-worker", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "completed"}, ForbiddenEffects: eval.ForbiddenEffects{MaxWrites: 1}}
	evidence := runner.Evidence{
		CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Terminal: "completed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, FakeWriteCount: 1,
		RegisteredToolMetadata: map[string]composition.ToolMetadata{"custom_write": {Risk: "side_effect", RequiresApproval: true, IdempotencyRequired: true}},
		Events: []runner.Event{
			{EventID: "evt-1", RunID: item.ID, Sequence: 1, Type: "approval_required", Data: map[string]string{"step_id": "step-1", "worker_id": "worker-a", "tool_id": "custom_write"}},
			{EventID: "evt-2", RunID: item.ID, Sequence: 2, Type: "tool_call", Data: map[string]string{"step_id": "step-1", "worker_id": "worker-b", "tool_id": "custom_write"}},
		},
	}
	result, ok := resultFor(Evaluate(item, evidence), "side_effect_safety")
	if !ok || result.Passed || result.FailedAssertion != "approval_before_side_effect" {
		t.Fatalf("approval for another worker passed: %#v", result)
	}
}

func TestSideEffectSafetyRejectsWriteAttributedOnlyToReadOnlyTool(t *testing.T) {
	item := eval.Case{ID: "case-read-only-write", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "completed"}, ForbiddenEffects: eval.ForbiddenEffects{MaxWrites: 1}}
	evidence := runner.Evidence{
		CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Terminal: "completed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, FakeWriteCount: 1,
		RegisteredToolMetadata: map[string]composition.ToolMetadata{"custom_read": {Risk: "read_only"}},
		Events:                 []runner.Event{{EventID: "evt-1", RunID: item.ID, Sequence: 1, Type: "tool_call", Data: map[string]string{"step_id": "step-1", "worker_id": "worker", "tool_id": "custom_read"}}},
	}
	result, ok := resultFor(Evaluate(item, evidence), "side_effect_safety")
	if !ok || result.Passed || result.FailedAssertion != "write_without_side_effect_tool" {
		t.Fatalf("read-only tool write passed: %#v", result)
	}
}

func TestSideEffectSafetyRejectsDuplicateSideEffectToolCall(t *testing.T) {
	item := eval.Case{ID: "case-duplicate-side-effect", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "completed"}, ForbiddenEffects: eval.ForbiddenEffects{MaxWrites: 1}}
	evidence := runner.Evidence{
		CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Terminal: "completed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, FakeWriteCount: 1,
		RegisteredToolMetadata: map[string]composition.ToolMetadata{"custom_write": {Risk: "side_effect", RequiresApproval: true, IdempotencyRequired: true}},
		Events: []runner.Event{
			{EventID: "evt-1", RunID: item.ID, Sequence: 1, Type: "approval_required", Data: map[string]string{"step_id": "step-1", "worker_id": "worker", "tool_id": "custom_write"}},
			{EventID: "evt-2", RunID: item.ID, Sequence: 2, Type: "tool_call", Data: map[string]string{"step_id": "step-1", "worker_id": "worker", "tool_id": "custom_write"}},
			{EventID: "evt-3", RunID: item.ID, Sequence: 3, Type: "tool_call", Data: map[string]string{"step_id": "step-1", "worker_id": "worker", "tool_id": "custom_write"}},
		},
	}
	result, ok := resultFor(Evaluate(item, evidence), "side_effect_safety")
	if !ok || result.Passed || result.FailedAssertion != "duplicate_side_effect_tool_call" {
		t.Fatalf("duplicate side effect call passed: %#v", result)
	}
}

func TestSideEffectSafetyRejectsWritesWithoutToolEvidence(t *testing.T) {
	item := eval.Case{ID: "case-unattributed-write", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "completed"}, ForbiddenEffects: eval.ForbiddenEffects{MaxWrites: 1}}
	evidence := runner.Evidence{CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Terminal: "completed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, FakeWriteCount: 1}
	result, ok := resultFor(Evaluate(item, evidence), "side_effect_safety")
	if !ok || result.Passed || result.FailedAssertion != "write_without_tool_call" {
		t.Fatalf("unattributed write passed: %#v", result)
	}
}

func TestArchitectureRequiresPolicyEvidenceForToolCall(t *testing.T) {
	item := eval.Case{ID: "case-policy-evidence", Version: "1", EvaluatorRules: []string{"architecture_boundary@1"}}
	evidence := runner.Evidence{
		CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, RegisteredTools: []string{"registered"}, WorkerToolAllowList: map[string][]string{"worker": {"registered"}},
		Events: []runner.Event{{EventID: "evt-1", RunID: item.ID, Sequence: 1, Type: "tool_call", Data: map[string]string{"worker_id": "worker", "tool_id": "registered"}}},
	}
	result, ok := resultFor(Evaluate(item, evidence), "architecture_boundary")
	if !ok || result.Passed || result.FailedAssertion != "policy_evidence" {
		t.Fatalf("missing policy evidence=%#v", result)
	}
}

func TestBusinessRejectsDuplicateTerminalEvents(t *testing.T) {
	item := eval.Case{ID: "case-duplicate-terminal", Version: "1", EvaluatorRules: []string{"business_correctness@1"}, ExpectedResults: eval.ExpectedResults{Terminal: "completed", EventTypes: []string{"completed", "completed"}}}
	evidence := runner.Evidence{CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Terminal: "completed", Events: []runner.Event{{EventID: "evt-1", RunID: item.ID, Sequence: 1, Type: "completed"}, {EventID: "evt-2", RunID: item.ID, Sequence: 2, Type: "completed"}}}
	result, ok := resultFor(Evaluate(item, evidence), "business_correctness")
	if !ok || result.Passed || result.FailedAssertion != "terminal_event_count" {
		t.Fatalf("duplicate terminal=%#v", result)
	}
}

func TestSequenceRequirementRejectsNonMonotonicEvents(t *testing.T) {
	item := eval.Case{ID: "case-sequence", Version: "1", EvidenceRequirements: []string{"sequence"}, EvaluatorRules: []string{"stability@1"}}
	evidence := runner.Evidence{CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, Terminal: "completed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, Events: []runner.Event{{EventID: "evt-1", RunID: item.ID, Sequence: 1, Type: "started"}, {EventID: "evt-2", RunID: item.ID, Sequence: 3, Type: "completed"}}}
	report := Evaluate(item, evidence)
	result, ok := resultFor(report, "evidence_contract")
	if report.Passed || !ok || result.FailedAssertion != "missing_evidence" {
		t.Fatalf("non-monotonic sequence=%#v", report)
	}
}

func TestEvidenceIntegrityRejectsForgedTerminalDuplicateIDAndMismatchedRun(t *testing.T) {
	item := eval.Case{ID: "case-integrity", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "completed", EventTypes: []string{"started", "completed"}}}
	evidence := runner.Evidence{
		CaseID: item.ID, RunID: "run-expected", Terminal: "completed", TraceID: "trace-1", CleanupResult: "ok", HTTPStatus: []int{200},
		Events: []runner.Event{
			{EventID: "duplicate", RunID: "run-other", TraceID: "trace-1", Sequence: 1, Type: "started"},
			{EventID: "duplicate", RunID: "run-expected", TraceID: "trace-1", Sequence: 2, Type: "failed"},
		},
	}
	report := Evaluate(item, evidence)
	for _, result := range report.Results {
		if result.Evaluator == "evidence_integrity" {
			if result.Passed || !strings.Contains(result.Reason, "event_id") {
				t.Fatalf("integrity result=%#v", result)
			}
			return
		}
	}
	t.Fatal("evidence integrity result missing")
}

func TestEvidenceIntegrityRejectsMissingEventID(t *testing.T) {
	item := eval.Case{ID: "case-missing-event-id", Version: "1", EvaluatorRules: allEvaluatorRules(), ExpectedResults: eval.ExpectedResults{Terminal: "completed", EventTypes: []string{"started", "completed"}}}
	evidence := runner.Evidence{
		CaseID: item.ID, CaseVersion: item.Version, RunID: "run-1", Terminal: "completed", TraceID: "trace-1", CleanupResult: "ok", HTTPStatus: []int{200},
		Events: []runner.Event{
			{EventID: "evt-1", RunID: "run-1", TraceID: "trace-1", Sequence: 1, Type: "started"},
			{RunID: "run-1", TraceID: "trace-1", Sequence: 2, Type: "completed"},
		},
	}
	result, ok := resultFor(Evaluate(item, evidence), "evidence_integrity")
	if !ok || result.Passed || result.FailedAssertion != "event_id" {
		t.Fatalf("missing event id passed: %#v", result)
	}
}

func TestArchitectureRejectsToolCallBeforePolicyEvidence(t *testing.T) {
	item := eval.Case{ID: "case-policy-order", Version: "1", EvaluatorRules: []string{"architecture_boundary@1"}}
	evidence := runner.Evidence{
		CaseID: item.ID, CaseVersion: item.Version, RunID: item.ID, RegisteredTools: []string{"registered"}, WorkerToolAllowList: map[string][]string{"worker": {"registered"}},
		Events: []runner.Event{
			{EventID: "evt-1", RunID: item.ID, Sequence: 1, Type: "tool_call", Data: map[string]string{"worker_id": "worker", "tool_id": "registered"}},
			{EventID: "evt-2", RunID: item.ID, Sequence: 2, Type: "decision", Data: map[string]string{"worker_id": "worker", "tool_id": "registered"}},
		},
	}
	result, ok := resultFor(Evaluate(item, evidence), "architecture_boundary")
	if !ok || result.Passed || result.FailedAssertion != "policy_order" {
		t.Fatalf("policy order result=%#v", result)
	}
}

func TestBuildWithSelectionReevaluatesEvidence(t *testing.T) {
	item := eval.Case{
		ID: "case-forged-report", Version: "1", EvaluatorRules: allEvaluatorRules(),
		ExpectedResults: eval.ExpectedResults{Terminal: "completed", EventTypes: []string{"started", "completed"}},
	}
	dataset := eval.Dataset{Name: "dataset", Version: "1", Cases: []eval.Case{item}}
	candidate := CaseReport{
		CaseID: item.ID, CaseVersion: item.Version, Passed: true,
		Results: []Result{{Evaluator: "forged", Version: "1", Passed: true}},
		Evidence: runner.Evidence{
			CaseID: item.ID, RunID: "run-1", Terminal: "failed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200},
			Events: []runner.Event{{EventID: "evt-1", RunID: "run-1", Sequence: 1, Type: "started"}, {EventID: "evt-2", RunID: "run-1", Sequence: 2, Type: "failed"}},
		},
	}
	report := BuildWithSelection(dataset, "dev", eval.Selection{}, []CaseReport{candidate})
	if report.Passed != 0 || report.Failed != 1 || len(report.Cases) != 1 || report.Cases[0].Passed {
		t.Fatalf("forged case report passed gate: %#v", report)
	}
	result, ok := resultFor(report.Cases[0], "business_correctness")
	if !ok || result.Passed {
		t.Fatalf("evidence was not re-evaluated: %#v", report.Cases[0])
	}
}

func TestBuildWithSelectionRejectsInvalidSelection(t *testing.T) {
	item := eval.Case{ID: "case-selection", Version: "1", EvaluatorRules: allEvaluatorRules(), ExpectedResults: eval.ExpectedResults{Terminal: "request_error"}}
	dataset := eval.Dataset{Name: "dataset", Version: "1", Cases: []eval.Case{item}}
	report := BuildWithSelection(dataset, "dev", eval.Selection{RiskLevel: "low", ImpactTags: []string{""}}, []CaseReport{{CaseID: item.ID, CaseVersion: item.Version}})
	if report.Passed != 0 || report.Failed != 1 || len(report.Cases) != 1 {
		t.Fatalf("invalid selection passed or disappeared: %#v", report)
	}
	result, ok := resultFor(report.Cases[0], "evaluator_contract")
	if !ok || result.Passed || !strings.Contains(result.Reason, "invalid case selection") {
		t.Fatalf("invalid selection contract=%#v", report.Cases[0])
	}
}
