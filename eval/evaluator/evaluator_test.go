package evaluator

import (
	"bufio"
	"os"
	"testing"

	eval "github.com/observer-mimiron/supervisor-template/eval"
	"github.com/observer-mimiron/supervisor-template/eval/runner"
)

func TestEvaluateReadOnlyEvidence(t *testing.T) {
	item := eval.Case{ID: "case-1", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "completed", EventTypes: []string{"started", "completed"}}, ForbiddenEffects: eval.ForbiddenEffects{MaxWrites: 0}}
	evidence := runner.Evidence{CaseID: item.ID, RunID: item.ID, TraceID: "trace", Terminal: "completed", CleanupResult: "ok", HTTPStatus: []int{200}, Events: []runner.Event{{Type: "started"}, {Type: "completed"}}}
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
	evidence := runner.Evidence{CaseID: item.ID, Terminal: "completed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, Events: []runner.Event{{Type: "text"}, {Type: "completed"}}, Result: map[string]any{"count": 4, "customer_ids": []any{"cust-001"}, "spend_365d_total": 6200}}
	if result := Evaluate(item, evidence).Results[0]; !result.Passed {
		t.Fatalf("matching structured result failed: %#v", result)
	}
	evidence.Result["count"] = 3
	result := Evaluate(item, evidence).Results[0]
	if result.Passed || result.FailedAssertion != "result" {
		t.Fatalf("mismatched structured result=%#v", result)
	}
}

func TestStabilityChecksRetryBudgetAndStepReference(t *testing.T) {
	item := eval.Case{ID: "case-stability", Version: "1", RetryBudget: 1}
	evidence := runner.Evidence{CaseID: item.ID, RunID: "run-1", Error: "failed", Terminal: "failed", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{200}, Retries: 2, FailedStepID: "run-1:step-1"}
	result := Evaluate(item, evidence).Results[3]
	if result.Passed || result.FailedAssertion != "retry_budget" || result.EvidenceRef != "case-stability#run-1:step-1" {
		t.Fatalf("retry stability=%#v", result)
	}
	evidence.Retries = 0
	evidence.RepeatChecked = true
	evidence.RepeatVerdictStable = false
	result = Evaluate(item, evidence).Results[3]
	if result.Passed || result.FailedAssertion != "repeat_verdict" {
		t.Fatalf("repeat stability=%#v", result)
	}
}

func TestExpectedRequestErrorHasNoFailureAssertion(t *testing.T) {
	item := eval.Case{ID: "case-request-error", Version: "1", ExpectedResults: eval.ExpectedResults{Terminal: "request_error"}}
	evidence := runner.Evidence{CaseID: item.ID, Error: "HTTP 400", TraceID: "trace", CleanupResult: "ok", HTTPStatus: []int{400}}
	result := Evaluate(item, evidence).Results[3]
	if !result.Passed || result.FailedAssertion != "" || result.FailureTaxonomy != "" {
		t.Fatalf("expected request error stability=%#v", result)
	}
}

func TestArchitectureUsesRuntimeSnapshotAndPolicyEvidence(t *testing.T) {
	item := eval.Case{ID: "case-architecture", Version: "1"}
	evidence := runner.Evidence{
		CaseID: item.ID, RunID: item.ID, RegisteredTools: []string{"registered"},
		WorkerToolAllowList: map[string][]string{"worker": {"registered"}},
		Events: []runner.Event{
			{Type: "decision", Data: map[string]string{"worker_id": "worker", "tool_id": "registered"}},
			{Type: "tool_call", Data: map[string]string{"worker_id": "worker", "tool_id": "registered"}},
		},
	}
	result := Evaluate(item, evidence).Results[1]
	if !result.Passed || result.FailedAssertion != "" {
		t.Fatalf("runtime snapshot architecture result=%#v", result)
	}
	evidence.RegisteredTools = []string{"different"}
	result = Evaluate(item, evidence).Results[1]
	if result.Passed || result.FailedAssertion != "registered_tool" || result.FailureTaxonomy != "architecture" {
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
	path := t.TempDir() + "/report.jsonl"
	report := Report{DatasetName: "dataset", DatasetVersion: "1", CodeVersion: "dev", Cases: []CaseReport{{CaseID: "case-1", CaseVersion: "1"}}}
	if err := WriteJSONL(path, report); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	lines := 0
	for scanner.Scan() {
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lines != 2 {
		t.Fatalf("JSONL lines = %d, want 2", lines)
	}
}
