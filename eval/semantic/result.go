package semantic

import "context"

// A semantic result is advisory evidence attached to a Case report. It has its
// own execution and acceptance status and it never borrows the hard vocabulary:
// there is no semantic "passed", and no semantic outcome can change a hard
// result, a baseline classification or an exit code.
//
// The record carries identities (digests), statuses and closed codes only. It
// never carries material text, raw model output or free-form reasons, so a
// report stays reviewable without re-publishing what was judged.

// Result is one case's semantic outcome.
type Result struct {
	Dimension            Dimension        `json:"dimension"`
	SampleID             string           `json:"sample_id,omitempty"`
	MaterialDigest       string           `json:"material_digest,omitempty"`
	ScorerDigest         string           `json:"scorer_digest,omitempty"`
	CalibrationDigest    string           `json:"calibration_digest,omitempty"`
	ExecutionStatus      ExecutionStatus  `json:"execution_status"`
	AcceptanceStatus     AcceptanceStatus `json:"acceptance_status"`
	Label                *Label           `json:"label"`
	DiagnosticPrediction *Label           `json:"diagnostic_prediction,omitempty"`
	// ReasonCode is empty only for a sample the material contract declares not
	// applicable: that is not a failure, and the frozen reason set has no code
	// for it.
	ReasonCode ReasonCode `json:"reason_code,omitempty"`
}

// Unverified builds an unverified result with one closed reason.
func Unverified(reason ReasonCode) Result {
	return Result{
		Dimension: DimensionAnswerRelevanceValue, ExecutionStatus: ExecutionUnverified,
		AcceptanceStatus: AcceptanceNotCalibrated, ReasonCode: reason,
	}
}

// EvaluationRequest is everything needed to produce one semantic result.
type EvaluationRequest struct {
	// Material is nil when the case has no independent evaluation material.
	Material *Material
	// Mode is the configured judge mode.
	Mode JudgeMode
	// Reuse is the calibration decision for this run.
	Reuse ReuseCheck
	// Runner is the budgeted judge. It is nil when no call is possible, and it is
	// only used when Reuse.Allowed is true.
	Runner *BudgetedJudge
	// CalibrationDigest identifies the calibration the result is bound to.
	CalibrationDigest string
	// ScorerDigest identifies the scorer the result is bound to.
	ScorerDigest string
	// Unavailable is the session-level closed reason when the semantic layer
	// could not be assembled at all (disabled, budget_missing,
	// credential_missing, calibration_mismatch, ...). It explains an absent
	// label more precisely than "no material", so it takes precedence whenever
	// the layer, not the material, is the cause.
	Unavailable ReasonCode
}

// Evaluate produces one semantic result and never returns an error.
//
// Every failure becomes a closed `unverified` state, because a semantic problem
// must not create, hide or upgrade a hard failure. A formal label is written only
// when the calibration explicitly authorizes this scorer in this run.
func Evaluate(ctx context.Context, request EvaluationRequest) Result {
	result := Result{
		Dimension: DimensionAnswerRelevanceValue, ExecutionStatus: ExecutionUnverified,
		AcceptanceStatus: AcceptanceNotCalibrated, CalibrationDigest: request.CalibrationDigest,
		ScorerDigest: request.ScorerDigest,
	}
	if request.Material == nil {
		result.ReasonCode = request.Unavailable
		if result.ReasonCode == "" {
			result.ReasonCode = ReasonMaterialMissing
		}
		return result
	}
	material := *request.Material
	result.SampleID = material.SampleID
	result.MaterialDigest = material.MaterialDigest
	if err := material.Validate(); err != nil {
		result.ReasonCode = ReasonOf(err)
		return result
	}
	if material.Applicability == ApplicabilityNotApplicable {
		// The sample contract requires no public answer, so nothing is evaluated
		// and nothing failed.
		return result
	}
	if request.Mode == JudgeModeOff {
		result.ReasonCode = request.Unavailable
		if result.ReasonCode == "" {
			result.ReasonCode = ReasonDisabled
		}
		return result
	}
	if !request.Reuse.Allowed {
		result.ReasonCode = request.Unavailable
		if result.ReasonCode == "" {
			result.ReasonCode = request.Reuse.Reason
		}
		if result.ReasonCode == "" {
			result.ReasonCode = ReasonNotCalibrated
		}
		result.AcceptanceStatus = request.Reuse.Status
		return result
	}
	result.AcceptanceStatus = AcceptanceUsable
	if request.Runner == nil {
		result.ReasonCode = request.Unavailable
		if result.ReasonCode == "" {
			result.ReasonCode = ReasonDisabled
		}
		return result
	}
	invocation := request.Runner.Invoke(ctx, material)
	if invocation.Outcome != CandidatePrediction || invocation.Response == nil {
		// The calibration is usable; this invocation failed. The failure is
		// reported as a closed reason and no formal label is written.
		result.ReasonCode = invocation.Reason
		if result.ReasonCode == "" {
			result.ReasonCode = ReasonCallError
		}
		return result
	}
	result.ExecutionStatus = ExecutionEvaluated
	label := invocation.Response.Label
	result.Label = &label
	return result
}

// DiagnosticAllowed reports whether a validated but unauthorized prediction may
// be kept as a clearly unusable local diagnostic. A diagnostic is never a formal
// label and is never uploaded.
func DiagnosticAllowed(reuse ReuseCheck) bool {
	return !reuse.Allowed
}

// UnverifiedPointer is the unverified result as a pointer, for callers that
// attach an optional semantic object.
func UnverifiedPointer(reason ReasonCode) *Result {
	result := Unverified(reason)
	return &result
}
