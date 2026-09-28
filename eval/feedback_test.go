package eval

import (
	"strings"
	"testing"
	"time"
)

func TestFeedbackContractsValidateAndDigestWithoutRawPayload(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	risk := RiskRecord{RiskLevel: "high", ImpactTags: []string{"approval"}, DeclaredBy: "reviewer-1", DeclaredAt: now}
	if err := risk.Validate(); err != nil {
		t.Fatal(err)
	}
	disposition := &HumanDisposition{Kind: "implementation", Decision: "add_case", Reviewer: "reviewer-1", Rationale: "regression", At: now}
	failure := FailureRecord{CaseID: "case-1", CaseVersion: "1", CodeVersion: "dev", RunID: "run-1", Evaluator: "stability", EvaluatorVersion: "1", FailedAssertion: "terminal", EvidenceReference: "case-1", HumanDisposition: disposition}
	if err := failure.Validate(); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"message":"secret user payload"}`)
	judge := JudgeResult{RubricVersion: "r1", Score: .8, RiskLevel: "medium", Finding: "ok", EvidenceReference: "case-1", Confidence: .9, Recommendation: "review", RequiresHumanReview: true, Model: "local-fake", PromptVersion: "p1", InputDigest: Digest(raw), OutputDigest: Digest([]byte("result")), EvaluatedAt: now}
	if err := judge.Validate(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(judge.InputDigest, "secret") || len(judge.InputDigest) != 64 {
		t.Fatalf("digest leaked or has wrong length: %q", judge.InputDigest)
	}
}

func TestDecodeFeedbackRejectsUnknownAndDuplicateKeys(t *testing.T) {
	var record FailureRecord
	if err := DecodeFeedback([]byte(`{"case_id":"a","case_id":"b"}`), &record); err == nil {
		t.Fatal("duplicate key accepted")
	}
	if err := DecodeFeedback([]byte(`{"case_id":"a","unknown":true}`), &record); err == nil {
		t.Fatal("unknown key accepted")
	}
}
