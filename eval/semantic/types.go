// Package semantic owns the local, non-blocking answer_relevance@1 contract:
// evaluation materials, human labels and reviews, Judge input projection and
// response validation, calibration statistics and the offline calibration
// report.
//
// It deliberately owns no hard evaluation decision. Nothing in this package can
// change a hard Case verdict, a baseline classification, an exit code or a
// `human_disposition`; those stay in eval/evaluator and cmd/eval. A semantic
// result is advisory evidence with its own execution/acceptance status, and a
// formal label exists only when an identity-matched calibration says the scorer
// is usable.
//
// The package holds exactly one frozen dimension (answer_relevance@1), one
// closed label set and one closed reason-code set. It is not a scoring DSL, a
// plugin registry or a material platform, and it adds no third-party
// dependency.
package semantic

import (
	"errors"
	"fmt"
)

// DimensionAnswerRelevance is the only frozen dimension of this feature.
const DimensionAnswerRelevance = "answer_relevance@1"

// Dimension is a closed evaluation-dimension identifier.
type Dimension string

// Label is a closed human/Judge quality label. Material shortage, Judge failure
// and missing calibration are states, not extra labels.
type Label string

// ExecutionStatus is the closed "did a Judge produce a validated prediction"
// state. There is no separate "passed" status: a semantic label never has hard
// pass semantics.
type ExecutionStatus string

// AcceptanceStatus is the closed "may this Judge's label be relied on" state.
type AcceptanceStatus string

// ReasonCode is a closed machine-readable explanation. Free-form reasons are
// rejected because they cannot be aggregated, compared or redacted reliably.
type ReasonCode string

// Applicability states whether a sample contract requires a public answer.
type Applicability string

// NotApplicableReason is the closed justification for ApplicabilityNotApplicable.
// A model may never widen this set on its own.
type NotApplicableReason string

// Closed label values.
const (
	LabelRelevant      Label = "relevant"
	LabelIrrelevant    Label = "irrelevant"
	LabelNotApplicable Label = "not_applicable"
)

// Closed execution status values.
const (
	ExecutionEvaluated  ExecutionStatus = "evaluated"
	ExecutionUnverified ExecutionStatus = "unverified"
)

// Closed acceptance status values.
const (
	AcceptanceNotCalibrated AcceptanceStatus = "not_calibrated"
	AcceptanceUnusable      AcceptanceStatus = "unusable"
	AcceptanceUsable        AcceptanceStatus = "usable"
)

// Closed unverified reason codes.
const (
	ReasonDisabled            ReasonCode = "disabled"
	ReasonCredentialMissing   ReasonCode = "credential_missing"
	ReasonMaterialMissing     ReasonCode = "material_missing"
	ReasonInvalidMaterial     ReasonCode = "invalid_material"
	ReasonNotCalibrated       ReasonCode = "not_calibrated"
	ReasonCalibrationUnusable ReasonCode = "calibration_unusable"
	ReasonCalibrationMismatch ReasonCode = "calibration_mismatch"
	ReasonCallError           ReasonCode = "call_error"
	ReasonTimeout             ReasonCode = "timeout"
	ReasonBudgetMissing       ReasonCode = "budget_missing"
	ReasonBudgetExhausted     ReasonCode = "budget_exhausted"
	ReasonInvalidOutput       ReasonCode = "invalid_output"
	ReasonInvalidAnnotation   ReasonCode = "invalid_annotation"
	ReasonSensitiveInput      ReasonCode = "sensitive_input"
	ReasonInputTooLarge       ReasonCode = "input_too_large"
)

// Closed applicability values.
const (
	ApplicabilityApplicable    Applicability = "applicable"
	ApplicabilityNotApplicable Applicability = "not_applicable"
)

// Closed not-applicable justifications. A missing answer is never
// not_applicable: it is unverified/material_missing.
const (
	NotApplicableReasonNoAnswerRequired NotApplicableReason = "no_natural_language_answer_required"
)

// DimensionAnswerRelevanceValue is the typed frozen dimension.
const DimensionAnswerRelevanceValue Dimension = DimensionAnswerRelevance

var (
	labels = map[Label]bool{
		LabelRelevant: true, LabelIrrelevant: true, LabelNotApplicable: true,
	}
	executionStatuses = map[ExecutionStatus]bool{
		ExecutionEvaluated: true, ExecutionUnverified: true,
	}
	acceptanceStatuses = map[AcceptanceStatus]bool{
		AcceptanceNotCalibrated: true, AcceptanceUnusable: true, AcceptanceUsable: true,
	}
	reasonCodes = map[ReasonCode]bool{
		ReasonDisabled: true, ReasonCredentialMissing: true, ReasonMaterialMissing: true,
		ReasonInvalidMaterial: true, ReasonNotCalibrated: true, ReasonCalibrationUnusable: true,
		ReasonCalibrationMismatch: true, ReasonCallError: true, ReasonTimeout: true,
		ReasonBudgetMissing: true, ReasonBudgetExhausted: true, ReasonInvalidOutput: true,
		ReasonInvalidAnnotation: true, ReasonSensitiveInput: true, ReasonInputTooLarge: true,
	}
	applicabilities = map[Applicability]bool{
		ApplicabilityApplicable: true, ApplicabilityNotApplicable: true,
	}
	notApplicableReasons = map[NotApplicableReason]bool{
		NotApplicableReasonNoAnswerRequired: true,
	}
)

// ValidLabel reports whether the value is one of the three frozen labels.
func ValidLabel(value Label) bool { return labels[value] }

// ValidExecutionStatus reports whether the value is a frozen execution status.
func ValidExecutionStatus(value ExecutionStatus) bool { return executionStatuses[value] }

// ValidAcceptanceStatus reports whether the value is a frozen acceptance status.
func ValidAcceptanceStatus(value AcceptanceStatus) bool { return acceptanceStatuses[value] }

// ValidReasonCode reports whether the value is a frozen reason code.
func ValidReasonCode(value ReasonCode) bool { return reasonCodes[value] }

// ValidApplicability reports whether the value is a frozen applicability state.
func ValidApplicability(value Applicability) bool { return applicabilities[value] }

// ValidNotApplicableReason reports whether the value is a frozen justification.
func ValidNotApplicableReason(value NotApplicableReason) bool {
	return notApplicableReasons[value]
}

// ValidDimension reports whether the value is the frozen dimension.
func ValidDimension(value Dimension) bool { return value == DimensionAnswerRelevanceValue }

// AllLabels returns the frozen label set in a stable order.
func AllLabels() []Label { return []Label{LabelRelevant, LabelIrrelevant, LabelNotApplicable} }

// AllReasonCodes returns the frozen reason-code set in a stable order.
func AllReasonCodes() []ReasonCode {
	return []ReasonCode{
		ReasonDisabled, ReasonCredentialMissing, ReasonMaterialMissing, ReasonInvalidMaterial,
		ReasonNotCalibrated, ReasonCalibrationUnusable, ReasonCalibrationMismatch, ReasonCallError,
		ReasonTimeout, ReasonBudgetMissing, ReasonBudgetExhausted, ReasonInvalidOutput,
		ReasonInvalidAnnotation, ReasonSensitiveInput, ReasonInputTooLarge,
	}
}

// AllAcceptanceStatuses returns the frozen acceptance statuses in a stable order.
func AllAcceptanceStatuses() []AcceptanceStatus {
	return []AcceptanceStatus{AcceptanceNotCalibrated, AcceptanceUnusable, AcceptanceUsable}
}

// Rejection is a closed, reportable failure of a local semantic input. It is not
// a hard evaluation failure: callers downgrade the semantic layer to
// `unverified` with Rejection.Reason and leave hard results untouched.
//
// Detail must never quote rejected content. A rejected sensitive value would
// otherwise be re-published through the very error that refused it.
type Rejection struct {
	Reason ReasonCode
	Detail string
}

func (r *Rejection) Error() string {
	if r.Detail == "" {
		return string(r.Reason)
	}
	return fmt.Sprintf("%s: %s", r.Reason, r.Detail)
}

// Reject builds a closed rejection.
func Reject(reason ReasonCode, format string, args ...any) error {
	return &Rejection{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// ReasonOf extracts the closed reason code from an error. Both a local-input
// Rejection and a JudgeError carry one; anything else falls back to
// invalid_material so an unclassified failure is never mistaken for a pass.
func ReasonOf(err error) ReasonCode {
	var rejection *Rejection
	if errors.As(err, &rejection) {
		return rejection.Reason
	}
	var failure *JudgeError
	if errors.As(err, &failure) {
		return failure.Reason
	}
	return ReasonInvalidMaterial
}

// validateClosedValues is the shared "unknown value" check used by record
// loaders so every rejection reports the frozen reason set instead of inventing
// per-loader error text.
func validateDimension(value Dimension) error {
	if !ValidDimension(value) {
		return Reject(ReasonInvalidMaterial, "unknown dimension")
	}
	return nil
}
