package runner

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	eval "github.com/observer-mimiron/supervisor-template/eval"
)

func TestCaptureResultProjectsOnlyExpectedFields(t *testing.T) {
	evidence := Evidence{Events: []Event{{Type: "text", Data: map[string]string{"content": `{"count":4,"customer_ids":["cust-001"],"secret":"drop"}`}}}}
	captureResult(&evidence, map[string]any{"count": 4, "customer_ids": []any{"cust-001"}})
	if len(evidence.Result) != 2 || evidence.Result["count"] != float64(4) {
		t.Fatalf("projected result=%#v", evidence.Result)
	}
	if _, ok := evidence.Result["secret"]; ok {
		t.Fatal("raw undeclared result field was retained")
	}
}

func TestTokenForSubjectUsesDistinctSyntheticCredential(t *testing.T) {
	if got := tokenForSubject("demo-user", "demo-token"); got != "demo-token" {
		t.Fatalf("default token=%q", got)
	}
	if got := tokenForSubject("demo-user-2", "demo-token"); got != "demo-token-demo-user-2" {
		t.Fatalf("variant token=%q", got)
	}
}

func TestMergeEventsUsesTerminalStepAsFailureReference(t *testing.T) {
	evidence := Evidence{}
	mergeEvents(&evidence, []Event{
		{Type: "progress", Data: map[string]string{"step_id": "run-1:step-1", "status": "succeeded"}},
		{Type: "failed", Data: map[string]string{"step_id": "run-1:step-2"}},
	}, map[string]bool{})
	if evidence.FailedStepID != "run-1:step-2" {
		t.Fatalf("failed step reference=%q", evidence.FailedStepID)
	}
}

func TestReadDiagnosticLogsProjectsSafeFieldsOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	data := `{"level":"INFO","msg":"runtime.tool","phase":"tool","run_id":"run-1"}` + "\n" + `{"level":"WARN","msg":"runtime.error","phase":"tool","error_code":"TOOL_TIMEOUT"}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	evidence, err := readDiagnosticLogs(path)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Available || evidence.RecordCount != 2 || !evidence.RedactionPassed || len(evidence.Phases) != 1 || len(evidence.ErrorCodes) != 1 {
		t.Fatalf("unexpected diagnostic evidence=%#v", evidence)
	}
}

func TestReadDiagnosticLogsRejectsSensitiveFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.jsonl")
	if err := os.WriteFile(path, []byte(`{"phase":"tool","sql":"SELECT secret"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readDiagnosticLogs(path); err == nil {
		t.Fatal("sensitive diagnostic field was accepted")
	}
}

func TestRunCaseCapturesDiagnosticLogsWhenRequested(t *testing.T) {
	runner := New("../../config.example.toml", "test", DefaultToken)
	item := eval.Case{
		ID: "runner-log-evidence", Version: "1", Preconditions: eval.Preconditions{Fixture: "synthetic_audience_v1", Subject: "demo-user"},
		RequestSteps:    []eval.RequestStep{{Action: "chat", Message: "分析沉睡客户", ConversationID: "runner-log-evidence"}},
		ExpectedResults: eval.ExpectedResults{Terminal: "completed"}, Postconditions: eval.Postconditions{Logs: &eval.LogPostcondition{MinRecords: 1}}, Timeout: "5s",
	}
	evidence, err := runner.RunCase(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Logs.Available || evidence.Logs.RecordCount == 0 || !evidence.Logs.RedactionPassed {
		t.Fatalf("diagnostic logs were not captured: %#v", evidence.Logs)
	}
}

func readOnlyCase(id string) eval.Case {
	return eval.Case{
		ID: id, Version: "1", Preconditions: eval.Preconditions{Fixture: "synthetic_audience_v1", Subject: "demo-user"},
		RequestSteps:    []eval.RequestStep{{Action: "chat", Message: "分析沉睡客户", ConversationID: id}},
		ExpectedResults: eval.ExpectedResults{Terminal: "completed"}, Timeout: "5s",
	}
}

// The local Runtime Runner must stay fake even when the process environment
// selects a real provider, and the profile it reports must say so.
func TestRunCaseStaysFakeWhenEnvironmentSelectsRealProvider(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "deepseek")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("LLM_MODEL", "deepseek-chat")

	runner := New("../../config.example.toml", "provider-env-test", DefaultToken)
	evidence, err := runner.RunCase(context.Background(), readOnlyCase("runner-provider-env"))
	if err != nil {
		t.Fatalf("fake execution changed by provider environment: %v", err)
	}
	want := eval.RuntimeFakeProfile(eval.TierPR)
	if !reflect.DeepEqual(evidence.Profile, want) {
		t.Fatalf("profile=%#v, want runtime-fake %#v", evidence.Profile, want)
	}
	if evidence.Profile.ModelProvider == nil || *evidence.Profile.ModelProvider != eval.ProviderFake {
		t.Fatalf("provider=%v, want fake", evidence.Profile.ModelProvider)
	}
	if evidence.Terminal != "completed" {
		t.Fatalf("terminal=%q, want completed from the local fake model", evidence.Terminal)
	}
	if evidence.CleanupResult != "ok" {
		t.Fatalf("cleanup result=%q, want ok", evidence.CleanupResult)
	}
}

func TestRunCaseRejectsInvalidCaseContract(t *testing.T) {
	runner := New("../../config.example.toml", "test", DefaultToken)
	for _, test := range []struct {
		name string
		item eval.Case
		want string
	}{
		{name: "missing timeout", item: eval.Case{ID: "no-timeout", Preconditions: eval.Preconditions{Fixture: "synthetic_audience_v1"}}, want: "case timeout"},
		{name: "zero timeout", item: eval.Case{ID: "zero-timeout", Timeout: "0s", Preconditions: eval.Preconditions{Fixture: "synthetic_audience_v1"}}, want: "case timeout"},
		{name: "malformed fixture name is a setup error", item: eval.Case{ID: "bad-fixture", Timeout: "1s", Preconditions: eval.Preconditions{Fixture: "Not A Fixture"}}, want: "evaluation_setup_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runner.RunCase(context.Background(), test.item); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestRunCaseHonorsExpiredCaseDeadline(t *testing.T) {
	runner := New("../../config.example.toml", "test", DefaultToken)
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	item := readOnlyCase("runner-deadline")
	if _, err := runner.RunCase(ctx, item); err == nil {
		t.Fatal("expired Case deadline was not observed")
	}
}

func TestRetryPreCallHonorsBudget(t *testing.T) {
	preCallFailure := func() (struct{}, int, error) {
		return struct{}{}, 0, &preCallError{err: errors.New("marshal")}
	}
	attempts, retries := 0, 0
	if _, _, err := retryPreCall(2, &retries, func() (struct{}, int, error) {
		attempts++
		if attempts <= 2 {
			return preCallFailure()
		}
		return struct{}{}, http.StatusOK, nil
	}); err != nil || attempts != 3 || retries != 2 {
		t.Fatalf("budgeted retry attempts=%d retries=%d err=%v", attempts, retries, err)
	}

	attempts, retries = 0, 0
	if _, _, err := retryPreCall(0, &retries, func() (struct{}, int, error) {
		attempts++
		return preCallFailure()
	}); err == nil || attempts != 1 || retries != 0 {
		t.Fatalf("zero budget attempts=%d retries=%d err=%v", attempts, retries, err)
	}

	// A request that already reached the server is never retried, because the
	// outcome may be unknown.
	attempts, retries = 0, 0
	if _, _, err := retryPreCall(3, &retries, func() (struct{}, int, error) {
		attempts++
		return struct{}{}, http.StatusInternalServerError, errors.New("HTTP 500")
	}); err == nil || attempts != 1 || retries != 0 {
		t.Fatalf("post-call error attempts=%d retries=%d err=%v", attempts, retries, err)
	}
}

func TestRunCaseRejectsMismatchedExecutorProfile(t *testing.T) {
	item := readOnlyCase("runner-profile-mismatch")
	for _, test := range []struct {
		name    string
		profile eval.EvaluationProfile
	}{
		{name: "zero profile", profile: eval.EvaluationProfile{}},
		{name: "coding fake profile", profile: eval.EvaluationProfile{EvaluationPlane: eval.PlaneCoding, Scenario: eval.ScenarioCodingFake, Executor: eval.String(eval.ExecutorLocalFake), Verifier: eval.String(eval.VerifierDeterministic), NetworkMode: eval.NetworkDisabled, Tier: eval.TierPR}},
		{name: "runtime real profile", profile: eval.EvaluationProfile{EvaluationPlane: eval.PlaneRuntime, Scenario: eval.ScenarioRuntimeReal, ModelProvider: eval.String(eval.ProviderDeepSeek), ModelName: eval.String("deepseek-chat"), Executor: eval.String(eval.ExecutorLocalFake), Verifier: eval.String(eval.VerifierDeterministic), NetworkMode: eval.NetworkEnabled, Tier: eval.TierNightly}},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := New("../../config.example.toml", "test", DefaultToken)
			runner.Profile = test.profile
			evidence, err := runner.RunCase(context.Background(), item)
			if err == nil || !strings.Contains(err.Error(), "evaluation_setup_error") {
				t.Fatalf("error=%v, want evaluation_setup_error", err)
			}
			if evidence.Terminal != "" || len(evidence.Events) != 0 || evidence.CleanupResult != "" {
				t.Fatalf("mismatched profile still executed the case: %#v", evidence)
			}
		})
	}
}

func TestEvidenceStaysWithinBoundedFields(t *testing.T) {
	runner := New("../../config.example.toml", "test", DefaultToken)
	item := readOnlyCase("runner-evidence-boundary")
	item.Postconditions = eval.Postconditions{Logs: &eval.LogPostcondition{MinRecords: 1}}
	evidence, err := runner.RunCase(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"evaluation_profile": true, "run_id": true, "case_id": true, "case_version": true, "code_version": true,
		"evaluator_version": true, "fixture": true, "subject": true, "trace_id": true, "events": true,
		"terminal": true, "error": true, "http_statuses": true, "elapsed_ms": true, "retries": true,
		"result": true, "failed_step_id": true, "verdict_digest": true, "repeat_checked": true,
		"repeat_verdict_stable": true, "fake_write_count": true, "cleanup_result": true,
		"registered_tools": true, "worker_tool_allow_list": true, "registered_tool_metadata": true,
		"database": true, "logs": true,
	}
	for key := range decoded {
		if !allowed[key] {
			t.Fatalf("evidence gained an unbounded field %q", key)
		}
	}
	lowered := strings.ToLower(string(data))
	for _, marker := range []string{"authorization", "bearer ", "api_key", "apikey", "password", "secret", "-----begin", "select ", "insert into", "/home/", "/workspace/", "/etc/", "/root/", "/tmp/"} {
		if strings.Contains(lowered, marker) {
			t.Fatalf("evidence contains sensitive marker %q", marker)
		}
	}
	if strings.Contains(string(data), item.RequestSteps[0].Message) {
		t.Fatal("evidence retained the full request prompt")
	}
	if !reflect.DeepEqual(databaseEvidenceKeys(t), []string{"after_order_count", "after_order_digest", "available", "backend", "before_order_count", "before_order_digest"}) {
		t.Fatalf("DatabaseEvidence fields=%v, want only counts, backend and digests", databaseEvidenceKeys(t))
	}
	if !reflect.DeepEqual(diagnosticLogKeys(t), []string{"available", "error_codes", "phases", "record_count", "redaction_passed"}) {
		t.Fatalf("DiagnosticLogEvidence fields=%v, want only low-cardinality projections", diagnosticLogKeys(t))
	}
	if !evidence.Logs.Available || !evidence.Logs.RedactionPassed {
		t.Fatalf("log evidence was not captured and redacted: %#v", evidence.Logs)
	}
}

func databaseEvidenceKeys(t *testing.T) []string {
	t.Helper()
	return jsonFieldNames(t, DatabaseEvidence{})
}

func diagnosticLogKeys(t *testing.T) []string {
	t.Helper()
	return jsonFieldNames(t, DiagnosticLogEvidence{})
}

func jsonFieldNames(t *testing.T, value any) []string {
	t.Helper()
	typ := reflect.TypeOf(value)
	names := make([]string, 0, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = field.Name
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
