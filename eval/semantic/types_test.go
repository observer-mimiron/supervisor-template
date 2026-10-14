package semantic

import "testing"

// The closed value sets are the whole contract of this feature: a label that is
// not in the frozen set is not a weaker measurement, it is a different one. These
// tests pin the accepted set and, more importantly, that near-misses are refused
// instead of being coerced.

func TestFrozenLabelSet(t *testing.T) {
	for _, value := range []Label{"relevant", "irrelevant", "not_applicable"} {
		if !ValidLabel(value) {
			t.Fatalf("expected %q to be a valid label", value)
		}
	}
	// A missing answer or a Judge failure is a state, never a fourth quality label.
	for _, value := range []Label{"", "Relevant", "RELEVANT", "unknown", "unverified", "skipped", "not-applicable", "notapplicable", "relevant "} {
		if ValidLabel(value) {
			t.Fatalf("expected %q to be refused as a label", value)
		}
	}
	if len(AllLabels()) != 3 {
		t.Fatalf("expected exactly three labels, got %v", AllLabels())
	}
}

func TestFrozenExecutionStatusSet(t *testing.T) {
	for _, value := range []ExecutionStatus{"evaluated", "unverified"} {
		if !ValidExecutionStatus(value) {
			t.Fatalf("expected %q to be a valid execution status", value)
		}
	}
	// "passed" would give an advisory label hard semantics.
	for _, value := range []ExecutionStatus{"", "passed", "failed", "Evaluated", "error", "skipped"} {
		if ValidExecutionStatus(value) {
			t.Fatalf("expected %q to be refused as an execution status", value)
		}
	}
}

func TestFrozenAcceptanceStatusSet(t *testing.T) {
	for _, value := range []AcceptanceStatus{"not_calibrated", "unusable", "usable"} {
		if !ValidAcceptanceStatus(value) {
			t.Fatalf("expected %q to be a valid acceptance status", value)
		}
	}
	for _, value := range []AcceptanceStatus{"", "calibrated", "usable ", "USABLE", "trusted", "uncalibrated"} {
		if ValidAcceptanceStatus(value) {
			t.Fatalf("expected %q to be refused as an acceptance status", value)
		}
	}
	if len(AllAcceptanceStatuses()) != 3 {
		t.Fatalf("expected exactly three acceptance statuses, got %v", AllAcceptanceStatuses())
	}
}

func TestFrozenReasonCodeSet(t *testing.T) {
	expected := []ReasonCode{
		"disabled", "credential_missing", "material_missing", "invalid_material",
		"not_calibrated", "calibration_unusable", "calibration_mismatch", "call_error",
		"timeout", "budget_missing", "budget_exhausted", "invalid_output",
		"invalid_annotation", "sensitive_input", "input_too_large",
	}
	if len(expected) != 15 {
		t.Fatalf("the frozen reason set has 15 values, test lists %d", len(expected))
	}
	for _, value := range expected {
		if !ValidReasonCode(value) {
			t.Fatalf("expected %q to be a valid reason code", value)
		}
	}
	if got := AllReasonCodes(); len(got) != len(expected) {
		t.Fatalf("AllReasonCodes has %d values, want %d", len(got), len(expected))
	}
	// Free-form reasons would not aggregate, compare or redact reliably.
	for _, value := range []ReasonCode{"", "unknown", "Disabled", "no_credentials", "model_error", "out_of_budget", "reason text"} {
		if ValidReasonCode(value) {
			t.Fatalf("expected %q to be refused as a reason code", value)
		}
	}
}

func TestFrozenDimension(t *testing.T) {
	if !ValidDimension(DimensionAnswerRelevanceValue) {
		t.Fatal("answer_relevance@1 must be a valid dimension")
	}
	if DimensionAnswerRelevance != "answer_relevance@1" {
		t.Fatalf("unexpected dimension constant %q", DimensionAnswerRelevance)
	}
	for _, value := range []Dimension{"", "answer_relevance", "answer_relevance@2", "factual_correctness@1", "ANSWER_RELEVANCE@1"} {
		if ValidDimension(value) {
			t.Fatalf("expected %q to be refused as a dimension", value)
		}
	}
}

func TestFrozenApplicabilityAndNotApplicableReason(t *testing.T) {
	if !ValidApplicability(ApplicabilityApplicable) || !ValidApplicability(ApplicabilityNotApplicable) {
		t.Fatal("expected both applicability states to be valid")
	}
	for _, value := range []Applicability{"", "not-applicable", "na", "skipped"} {
		if ValidApplicability(value) {
			t.Fatalf("expected %q to be refused as applicability", value)
		}
	}
	if !ValidNotApplicableReason(NotApplicableReasonNoAnswerRequired) {
		t.Fatal("no_natural_language_answer_required must be the frozen not-applicable reason")
	}
	// "the model decided not to score it" is not a sample-contract justification.
	for _, value := range []NotApplicableReason{"", "model_decided", "no_answer", "material_missing", "irrelevant"} {
		if ValidNotApplicableReason(value) {
			t.Fatalf("expected %q to be refused as a not-applicable reason", value)
		}
	}
}
