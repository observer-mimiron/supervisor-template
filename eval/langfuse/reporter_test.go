package langfuse

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	eval "github.com/observer-mimiron/supervisor-template/eval"
	"github.com/observer-mimiron/supervisor-template/eval/evaluator"
	"github.com/observer-mimiron/supervisor-template/eval/runner"
)

func TestUploadReportSendsTraceLinkedScores(t *testing.T) {
	var got score
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/public/scores" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "pk" || password != "sk" {
			t.Fatalf("basic auth missing or incorrect")
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client, err := New(server.URL, "pk", "sk")
	if err != nil {
		t.Fatal(err)
	}
	report := evaluator.Report{CodeVersion: "code-1", Cases: []evaluator.CaseReport{{
		CaseID: "case-1", CaseVersion: "1", Evidence: runner.Evidence{TraceID: "trace-1"},
		Results: []evaluator.Result{{Evaluator: "business_correctness", Version: "2", Passed: true, Reason: "ok"}},
	}}}
	report.EvaluationProfile = eval.RuntimeFakeProfile(eval.TierPR)
	uploaded, err := client.UploadReport(context.Background(), report)
	if err != nil || uploaded != 1 {
		t.Fatalf("uploaded=%d err=%v", uploaded, err)
	}
	if got.TraceID != "trace-1" || got.Name != "business_correctness" || got.Value != 1 || got.DataType != "BOOLEAN" {
		t.Fatalf("unexpected score: %+v", got)
	}
	if got.Metadata["scenario"] != "runtime-fake" || got.Metadata["evaluation_plane"] != "runtime" || got.Metadata["tier"] != "pr" {
		t.Fatalf("profile metadata missing: %+v", got.Metadata)
	}
}

func TestUploadReportSkipsLocalEvidenceWithoutTrace(t *testing.T) {
	client, err := New("http://127.0.0.1:1", "pk", "sk")
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := client.UploadReport(context.Background(), evaluator.Report{Cases: []evaluator.CaseReport{{
		Evidence: runner.Evidence{}, Results: []evaluator.Result{{Evaluator: "stability", Passed: true}},
	}}})
	if err != nil || uploaded != 0 {
		t.Fatalf("uploaded=%d err=%v", uploaded, err)
	}
}

// FR-008: the sink must never receive prompts, tool payloads, SQL parameters,
// credentials or host paths. The evaluator's free-form Reason may legitimately
// contain evidence values for local debugging, so the upload must not forward it.
func TestUploadReportNeverForwardsEvidenceValues(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(raw))
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client, err := New(server.URL, "pk", "sk")
	if err != nil {
		t.Fatal(err)
	}
	report := evaluator.Report{CodeVersion: "code-1", Cases: []evaluator.CaseReport{{
		CaseID: "case-1", CaseVersion: "1", Evidence: runner.Evidence{TraceID: "trace-1"},
		Results: []evaluator.Result{
			{
				Evaluator: "business_correctness", Version: "2", Passed: false,
				Reason:           `expected terminal=completed/result=map[count:999], got result=map[count:4 customer_ids:[cust-001 cust-008] spend_365d_total:6200]`,
				FailedAssertion:  "result",
				FailureTaxonomy:  "business_expectation",
				EvidenceRef:      "case-1",
				HumanDisposition: nil,
			},
			{Evaluator: "stability", Version: "2", Passed: true, Reason: `terminal=completed trace=true elapsed_ms=3 retries=0/0`},
		},
	}}}
	report.EvaluationProfile = eval.RuntimeFakeProfile(eval.TierPR)
	uploaded, err := client.UploadReport(context.Background(), report)
	if err != nil || uploaded != 2 {
		t.Fatalf("uploaded=%d err=%v", uploaded, err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies=%d, want 2", len(bodies))
	}
	for _, body := range bodies {
		lowered := strings.ToLower(body)
		for _, marker := range []string{"cust-001", "cust-008", "customer_ids", "result=map", "spend_365d_total",
			"api_key", "authorization", "password", "-----begin", "/home/", "/workspace/", "/etc/"} {
			if strings.Contains(lowered, strings.ToLower(marker)) {
				t.Fatalf("uploaded score carries forbidden content %q: %s", marker, body)
			}
		}
	}

	// The failed score still has to be actionable from Langfuse alone, so the
	// comment carries the bounded structured decision instead of the prose.
	var failed score
	if err := json.Unmarshal([]byte(bodies[0]), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Comment != "failed_assertion=result taxonomy=business_expectation" {
		t.Fatalf("comment=%q, want the bounded structured decision", failed.Comment)
	}
	if failed.Value != 0 || failed.DataType != "BOOLEAN" {
		t.Fatalf("failed score lost its Boolean verdict: %+v", failed)
	}
	// Metadata must stay the closed bounded set.
	wantKeys := map[string]bool{
		"case_id": true, "case_version": true, "code_version": true, "evaluator_version": true,
		"evaluation_plane": true, "scenario": true, "tier": true,
	}
	if len(failed.Metadata) != len(wantKeys) {
		t.Fatalf("metadata=%v, want exactly %d bounded keys", failed.Metadata, len(wantKeys))
	}
	for key := range failed.Metadata {
		if !wantKeys[key] {
			t.Fatalf("metadata gained an unbounded key %q", key)
		}
	}

	// A passing score needs no comment at all.
	var passed score
	if err := json.Unmarshal([]byte(bodies[1]), &passed); err != nil {
		t.Fatal(err)
	}
	if passed.Comment != "" {
		t.Fatalf("passing score comment=%q, want empty", passed.Comment)
	}
}
