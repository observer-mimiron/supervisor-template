package semantic

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// The Judge port is deliberately narrow: one projected input in, one closed
// response out. There is no scoring plugin registry and no prompt DSL — a second
// implementation can be added when a second real consumer exists, not before.

// Judge scores one projected input.
//
// Implementations must return either a validated response or a *JudgeError
// carrying a closed reason. They must not retry: the caller owns the budget.
type Judge interface {
	Judge(ctx context.Context, input JudgeInput) (JudgeResponse, error)
}

// JudgeError is a closed, reportable Judge failure.
type JudgeError struct {
	Reason ReasonCode
	Detail string
}

func (e *JudgeError) Error() string {
	if e.Detail == "" {
		return string(e.Reason)
	}
	return fmt.Sprintf("%s: %s", e.Reason, e.Detail)
}

// JudgeFailure builds a closed Judge error.
func JudgeFailure(reason ReasonCode, format string, args ...any) error {
	return &JudgeError{Reason: reason, Detail: fmt.Sprintf(format, args...)}
}

// JudgeReasonOf extracts the closed reason from a Judge failure, defaulting to
// call_error.
//
// It also understands the *Rejection returned by the strict response parser:
// invalid provider output is an output problem, not a transport problem, and
// reporting it as call_error would hide which side failed.
func JudgeReasonOf(err error) ReasonCode {
	var failure *JudgeError
	if errors.As(err, &failure) {
		return failure.Reason
	}
	var rejection *Rejection
	if errors.As(err, &rejection) {
		return rejection.Reason
	}
	return ReasonCallError
}

// JudgeMode is the closed execution mode of the semantic layer.
type JudgeMode string

// Closed judge modes.
const (
	JudgeModeOff  JudgeMode = "off"
	JudgeModeFake JudgeMode = "fake"
	JudgeModeReal JudgeMode = "real"
)

// ParseJudgeMode validates a configured mode.
func ParseJudgeMode(value string) (JudgeMode, error) {
	switch JudgeMode(value) {
	case JudgeModeOff, JudgeModeFake, JudgeModeReal:
		return JudgeMode(value), nil
	}
	return "", Reject(ReasonDisabled, "semantic judge mode must be off, fake or real")
}

// Kind maps a mode onto the scorer kind it may authorize.
func (m JudgeMode) Kind() ScorerKind {
	if m == JudgeModeReal {
		return ScorerReal
	}
	return ScorerFake
}

// FakeJudge is a deterministic in-process Judge for offline contracts.
//
// Its answers are declared by the caller or fall back to the only claim a fake
// can honestly make: that a non-empty answer was produced. It is a test double,
// never evidence about answer quality.
type FakeJudge struct {
	// Declared maps a projected input key to an exact response.
	Declared map[string]JudgeResponse
	// DeclaredErrors maps a projected input key to a closed failure.
	DeclaredErrors map[string]*JudgeError
	// Fallback is returned when nothing matches. The zero value answers
	// relevant/addresses_intent.
	Fallback JudgeResponse
	// FallbackError, when set, fails every unmatched call.
	FallbackError *JudgeError

	mutex sync.Mutex
	calls int
}

var _ Judge = (*FakeJudge)(nil)

// Judge implements the port deterministically.
func (f *FakeJudge) Judge(_ context.Context, input JudgeInput) (JudgeResponse, error) {
	f.mutex.Lock()
	f.calls++
	f.mutex.Unlock()
	key := input.Key()
	if failure, ok := f.DeclaredErrors[key]; ok && failure != nil {
		return JudgeResponse{}, failure
	}
	if response, ok := f.Declared[key]; ok {
		if err := response.Validate(); err != nil {
			return JudgeResponse{}, JudgeFailure(ReasonInvalidOutput, "declared fake response is not a valid pair")
		}
		return response, nil
	}
	if f.FallbackError != nil {
		return JudgeResponse{}, f.FallbackError
	}
	response := f.Fallback
	if response.Label == "" && response.Reason == "" {
		response = JudgeResponse{Label: LabelRelevant, Reason: JudgeReasonAddressesIntent}
	}
	if err := response.Validate(); err != nil {
		return JudgeResponse{}, JudgeFailure(ReasonInvalidOutput, "declared fake fallback is not a valid pair")
	}
	return response, nil
}

// Calls reports how many invocations were attempted, so a zero-call path can be
// proven rather than assumed.
func (f *FakeJudge) Calls() int {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	return f.calls
}

// Invocation is the validated outcome of one planned Judge invocation.
type Invocation struct {
	MaterialDigest string
	Outcome        CandidateOutcome
	Response       *JudgeResponse
	Reason         ReasonCode
}

// BudgetedJudge executes planned invocations serially under one finite budget.
//
// It is shared by the two callers that may invoke a Judge — explicit candidate
// sampling and formal scoring — so the privacy and budget guards cannot drift
// apart between them.
type BudgetedJudge struct {
	Judge   Judge
	Budget  BudgetIdentity
	Timeout time.Duration

	mutex sync.Mutex
	calls int
}

// Invoke runs one planned invocation and always returns a closed outcome. A
// failure is reported, never retried: an automatic retry would spend budget the
// registration did not authorize.
func (b *BudgetedJudge) Invoke(ctx context.Context, material Material) Invocation {
	invocation := Invocation{MaterialDigest: material.MaterialDigest, Outcome: CandidateError}
	// Order matters for the zero-call guarantees: a disabled Judge and a refused
	// material are decided before any budget is consumed, so the reported call
	// count is exactly the number of invocations that reached the Judge.
	if b.Judge == nil {
		invocation.Reason = ReasonDisabled
		return invocation
	}
	// The projection is rebuilt here, immediately before the call, so nothing can
	// substitute a request body after validation.
	input, err := Project(material)
	if err != nil {
		invocation.Reason = ReasonOf(err)
		if invocation.Reason == ReasonInvalidMaterial && material.PublicAnswer == "" {
			invocation.Reason = ReasonMaterialMissing
		}
		return invocation
	}
	body, err := input.Bytes()
	if err != nil {
		invocation.Reason = ReasonInvalidMaterial
		return invocation
	}
	if len(body) > b.Budget.MaxInputBytes || len(body) > MaxProjectionTotalBytes {
		invocation.Reason = ReasonInputTooLarge
		return invocation
	}
	b.mutex.Lock()
	if b.calls >= b.Budget.MaxCalls {
		b.mutex.Unlock()
		invocation.Outcome = CandidateSkip
		invocation.Reason = ReasonBudgetExhausted
		return invocation
	}
	b.calls++
	b.mutex.Unlock()

	timeout := b.Timeout
	if timeout <= 0 {
		timeout = time.Duration(b.Budget.TimeoutMS) * time.Millisecond
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	response, err := b.Judge.Judge(ctx, input)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			invocation.Reason = ReasonTimeout
		default:
			invocation.Reason = JudgeReasonOf(err)
		}
		return invocation
	}
	if err := response.Validate(); err != nil {
		invocation.Reason = ReasonInvalidOutput
		return invocation
	}
	invocation.Outcome = CandidatePrediction
	invocation.Response = &response
	return invocation
}

// Calls reports the number of invocations that actually reached the Judge.
func (b *BudgetedJudge) Calls() int {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.calls
}

// ReuseCheck is the deterministic answer to "may this calibration authorize this
// scorer in this run".
type ReuseCheck struct {
	Allowed     bool
	Reason      ReasonCode
	Status      AcceptanceStatus
	Invalidated []string
}

// CheckCalibrationReuse validates a recorded calibration report against the run
// that wants to reuse it.
//
// A fake calibration can never authorize a real scorer, and any changed scoring
// input — model, parameters, prompt, rubric, projection, reference, materials,
// annotations, split, rules or environment — invalidates the old authorization
// and names what changed.
func CheckCalibrationReuse(report CalibrationReport, mode JudgeMode, expectedScorerDigest string, current CalibrationIdentity) ReuseCheck {
	check := ReuseCheck{Status: report.Acceptance.Status}
	if report.Acceptance.Status != AcceptanceUsable {
		if report.Acceptance.Status == AcceptanceNotCalibrated {
			check.Reason = ReasonNotCalibrated
		} else {
			check.Reason = ReasonCalibrationUnusable
		}
		return check
	}
	if report.Scorer.Kind != mode.Kind() {
		check.Reason = ReasonCalibrationMismatch
		return check
	}
	if expectedScorerDigest != "" && report.Scorer.Digest != expectedScorerDigest {
		check.Reason = ReasonCalibrationMismatch
		return check
	}
	invalidated := InvalidatedFields(report.Identity, current)
	if len(invalidated) > 0 {
		check.Reason = ReasonCalibrationMismatch
		check.Invalidated = invalidated
		return check
	}
	check.Allowed = true
	return check
}

// LoadCalibrationReport reads a strict JSON calibration report.
func LoadCalibrationReport(path string) (CalibrationReport, error) {
	var report CalibrationReport
	if err := DecodeJSONStrict(path, &report); err != nil {
		return CalibrationReport{}, err
	}
	if report.ReportSchemaVersion != CalibrationReportSchemaVersion {
		return CalibrationReport{}, Reject(ReasonCalibrationMismatch,
			"unsupported calibration report schema version %q", report.ReportSchemaVersion)
	}
	return report, nil
}

// LoadBudget reads a strict JSON finite budget.
func LoadBudget(path string) (BudgetIdentity, error) {
	var budget BudgetIdentity
	if err := DecodeJSONStrict(path, &budget); err != nil {
		return BudgetIdentity{}, err
	}
	if err := budget.Validate(); err != nil {
		return BudgetIdentity{}, err
	}
	return budget, nil
}
