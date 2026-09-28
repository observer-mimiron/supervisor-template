package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// RiskRecord is a human declaration used to select the deterministic Case set.
type RiskRecord struct {
	RiskLevel  string    `json:"risk_level"`
	ImpactTags []string  `json:"impact_tags"`
	DeclaredBy string    `json:"declared_by"`
	Rationale  string    `json:"rationale"`
	DeclaredAt time.Time `json:"declared_at"`
}

// HumanDisposition records why a failure is or is not fed back into a contract.
type HumanDisposition struct {
	Kind      string    `json:"kind"`
	Decision  string    `json:"decision"`
	Reviewer  string    `json:"reviewer"`
	Rationale string    `json:"rationale"`
	At        time.Time `json:"at"`
}

// FailureRecord keeps evaluator-level evidence without copying user payloads.
type FailureRecord struct {
	CaseID            string            `json:"case_id"`
	CaseVersion       string            `json:"case_version"`
	CodeVersion       string            `json:"code_version"`
	RunID             string            `json:"run_id"`
	Evaluator         string            `json:"evaluator_name"`
	EvaluatorVersion  string            `json:"evaluator_version"`
	FailedAssertion   string            `json:"failed_assertion"`
	EvidenceReference string            `json:"evidence_reference"`
	HumanDisposition  *HumanDisposition `json:"human_disposition,omitempty"`
}

// JudgeResult is advisory. It cannot change deterministic hard-gate results.
type JudgeResult struct {
	RubricVersion       string    `json:"rubric_version"`
	Score               float64   `json:"score"`
	RiskLevel           string    `json:"risk_level"`
	Finding             string    `json:"finding"`
	EvidenceReference   string    `json:"evidence_reference"`
	Confidence          float64   `json:"confidence"`
	Recommendation      string    `json:"recommendation"`
	RequiresHumanReview bool      `json:"requires_human_review"`
	Model               string    `json:"model"`
	PromptVersion       string    `json:"prompt_version"`
	InputDigest         string    `json:"input_digest"`
	OutputDigest        string    `json:"output_digest"`
	EvaluatedAt         time.Time `json:"evaluated_at"`
}

var failureKinds = map[string]bool{
	"business_expectation": true, "implementation": true, "architecture": true,
	"evaluator_rule": true, "case_data": true, "environment_dependency": true, "judge": true,
}

// Digest returns a stable non-reversible reference for evidence supplied to a Judge.
func Digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func (r RiskRecord) Validate() error {
	if !allowedRisks[r.RiskLevel] || strings.TrimSpace(r.DeclaredBy) == "" || r.DeclaredAt.IsZero() {
		return errors.New("risk record requires risk level, declarer and timestamp")
	}
	if len(r.ImpactTags) == 0 {
		return errors.New("risk record requires impact tags")
	}
	return nil
}

func (d HumanDisposition) Validate() error {
	if !failureKinds[d.Kind] || (d.Decision != "add_case" && d.Decision != "update_rule" && d.Decision != "update_rubric" && d.Decision != "close") {
		return errors.New("invalid human disposition")
	}
	if strings.TrimSpace(d.Reviewer) == "" || strings.TrimSpace(d.Rationale) == "" || d.At.IsZero() {
		return errors.New("human disposition requires reviewer, rationale and timestamp")
	}
	return nil
}

func (f FailureRecord) Validate() error {
	for name, value := range map[string]string{"case_id": f.CaseID, "case_version": f.CaseVersion, "code_version": f.CodeVersion, "run_id": f.RunID, "evaluator_name": f.Evaluator, "evaluator_version": f.EvaluatorVersion, "failed_assertion": f.FailedAssertion, "evidence_reference": f.EvidenceReference} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("failure record missing %s", name)
		}
	}
	if f.HumanDisposition != nil {
		return f.HumanDisposition.Validate()
	}
	return nil
}

func (j JudgeResult) Validate() error {
	if strings.TrimSpace(j.RubricVersion) == "" || strings.TrimSpace(j.RiskLevel) == "" || strings.TrimSpace(j.EvidenceReference) == "" || strings.TrimSpace(j.Model) == "" || strings.TrimSpace(j.PromptVersion) == "" || len(j.InputDigest) != 64 || len(j.OutputDigest) != 64 || j.EvaluatedAt.IsZero() {
		return errors.New("judge result is incomplete")
	}
	if j.Score < 0 || j.Score > 1 || j.Confidence < 0 || j.Confidence > 1 {
		return errors.New("judge score and confidence must be between 0 and 1")
	}
	if !allowedRisks[j.RiskLevel] {
		return errors.New("judge risk level is invalid")
	}
	return nil
}

// DecodeFeedback applies the same strict JSON boundary as the dataset loader.
func DecodeFeedback(data []byte, target any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("feedback has trailing JSON")
	}
	return nil
}
