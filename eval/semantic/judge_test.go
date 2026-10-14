package semantic

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type countingJudge struct {
	calls int
	block chan struct{}
}

func (c *countingJudge) Judge(ctx context.Context, _ JudgeInput) (JudgeResponse, error) {
	c.calls++
	if c.block != nil {
		select {
		case <-ctx.Done():
			return JudgeResponse{}, ctx.Err()
		case <-c.block:
		}
	}
	return JudgeResponse{Label: LabelRelevant, Reason: JudgeReasonAddressesIntent}, nil
}

func testBudget(calls, timeoutMS int) BudgetIdentity {
	budget := BudgetIdentity{MaxCalls: calls, TimeoutMS: timeoutMS, MaxInputBytes: MaxProjectionTotalBytes, MaxOutputBytes: 2048}
	budget.Digest = BudgetDigest(budget)
	return budget
}

func TestEveryFrozenLabelReasonPairIsAccepted(t *testing.T) {
	// relevant -> addresses_intent; irrelevant -> off_topic | generic_acknowledgement;
	// not_applicable -> not_applicable. Anything else is inconsistent.
	accepted := []JudgeResponse{
		{LabelRelevant, JudgeReasonAddressesIntent},
		{LabelIrrelevant, JudgeReasonOffTopic},
		{LabelIrrelevant, JudgeReasonGenericAcknowledgement},
		{LabelNotApplicable, JudgeReasonNotApplicable},
	}
	for _, response := range accepted {
		if err := response.Validate(); err != nil {
			t.Fatalf("%s/%s must be accepted: %v", response.Label, response.Reason, err)
		}
	}
	rejected := []JudgeResponse{
		{LabelRelevant, JudgeReasonOffTopic},
		{LabelRelevant, JudgeReasonGenericAcknowledgement},
		{LabelRelevant, JudgeReasonNotApplicable},
		{LabelIrrelevant, JudgeReasonAddressesIntent},
		{LabelIrrelevant, JudgeReasonNotApplicable},
		{LabelNotApplicable, JudgeReasonAddressesIntent},
		{Label("maybe"), JudgeReasonAddressesIntent},
		{LabelRelevant, JudgeReason("looks_fine")},
		{Label(""), JudgeReason("")},
	}
	for _, response := range rejected {
		err := response.Validate()
		if err == nil {
			t.Fatalf("%s/%s must be refused", response.Label, response.Reason)
		}
		if ReasonOf(err) != ReasonInvalidOutput {
			t.Fatalf("expected invalid_output, got %v", err)
		}
	}
	if len(JudgeReasons()) != 4 {
		t.Fatalf("the closed reason set has four values: %v", JudgeReasons())
	}
}

func TestResponseParsingRejectsEverythingOutsideTheContract(t *testing.T) {
	cases := map[string]string{
		"unknown field":     `{"label":"relevant","reason_code":"addresses_intent","confidence":0.9}`,
		"free-form reason":  `{"label":"relevant","reason_code":"looks relevant to me"}`,
		"duplicate key":     `{"label":"relevant","label":"irrelevant","reason_code":"addresses_intent"}`,
		"trailing value":    `{"label":"relevant","reason_code":"addresses_intent"}{"label":"irrelevant","reason_code":"off_topic"}`,
		"free-form text":    `The answer looks relevant.`,
		"array":             `[{"label":"relevant","reason_code":"addresses_intent"}]`,
		"empty":             `{}`,
		"not json":          `label=relevant`,
		"unknown label":     `{"label":"partially_relevant","reason_code":"addresses_intent"}`,
		"non-finite number": `{"label":"relevant","reason_code":"addresses_intent","score":NaN}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			response, err := ParseJudgeResponse([]byte(raw))
			if err == nil {
				t.Fatalf("%s must be refused, got %+v", name, response)
			}
			if ReasonOf(err) != ReasonInvalidOutput {
				t.Fatalf("expected invalid_output, got %v", err)
			}
			// Free-form provider text is never echoed back into an artifact.
			if strings.Contains(err.Error(), "looks relevant to me") {
				t.Fatalf("the rejection must not quote the rejected output: %v", err)
			}
		})
	}

	valid := `{"label":"irrelevant","reason_code":"generic_acknowledgement"}`
	response, err := ParseJudgeResponse([]byte(valid))
	if err != nil {
		t.Fatalf("valid output must parse: %v", err)
	}
	if response.Label != LabelIrrelevant || response.Reason != JudgeReasonGenericAcknowledgement {
		t.Fatalf("unexpected response: %+v", response)
	}
}

// Both reverse directions: the same hard evidence with a relevant answer and
// with an off-topic answer must be scoreable in either direction without the
// semantic layer touching the hard verdict.
func TestFakeJudgeScoresBothReverseDirections(t *testing.T) {
	relevant := materialWithAnswer(t, "符合沉睡客户筛选条件的合成客户共 12 位")
	irrelevant := materialWithAnswer(t, "今天适合选择绿色主题")
	relevantInput, err := Project(relevant)
	if err != nil {
		t.Fatal(err)
	}
	irrelevantInput, err := Project(irrelevant)
	if err != nil {
		t.Fatal(err)
	}
	judge := &FakeJudge{Declared: map[string]JudgeResponse{
		relevantInput.Key():   {Label: LabelRelevant, Reason: JudgeReasonAddressesIntent},
		irrelevantInput.Key(): {Label: LabelIrrelevant, Reason: JudgeReasonOffTopic},
	}}
	budgeted := &BudgetedJudge{Judge: judge, Budget: testBudget(4, 1000)}
	ctx := context.Background()

	first := budgeted.Invoke(ctx, relevant)
	if first.Outcome != CandidatePrediction || first.Response == nil || first.Response.Label != LabelRelevant {
		t.Fatalf("relevant answer: %+v", first)
	}
	second := budgeted.Invoke(ctx, irrelevant)
	if second.Outcome != CandidatePrediction || second.Response == nil || second.Response.Label != LabelIrrelevant {
		t.Fatalf("off-topic answer: %+v", second)
	}
	// The reverse direction is also expressible: the fake can declare a reverse
	// score, which is exactly the counterexample the isolation tests need.
	reverse := &FakeJudge{Declared: map[string]JudgeResponse{
		relevantInput.Key(): {Label: LabelIrrelevant, Reason: JudgeReasonGenericAcknowledgement},
	}}
	reversed := (&BudgetedJudge{Judge: reverse, Budget: testBudget(2, 1000)}).Invoke(ctx, relevant)
	if reversed.Response == nil || reversed.Response.Label != LabelIrrelevant {
		t.Fatalf("a declared reverse score must be returned: %+v", reversed)
	}
}

func TestFakeJudgeCountsCallsAndStopsAtTheBudget(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	judge := &FakeJudge{}
	budgeted := &BudgetedJudge{Judge: judge, Budget: testBudget(2, 1000)}
	ctx := context.Background()
	for index := 0; index < 2; index++ {
		if invocation := budgeted.Invoke(ctx, material); invocation.Outcome != CandidatePrediction {
			t.Fatalf("invocation %d: %+v", index, invocation)
		}
	}
	// Beyond the registered call count the planned invocation is a recorded skip,
	// not an extra request.
	overflow := budgeted.Invoke(ctx, material)
	if overflow.Outcome != CandidateSkip || overflow.Reason != ReasonBudgetExhausted {
		t.Fatalf("expected budget_exhausted skip, got %+v", overflow)
	}
	if judge.Calls() != 2 {
		t.Fatalf("the judge must be called exactly twice, got %d", judge.Calls())
	}
	if budgeted.Calls() != 2 {
		t.Fatalf("attempted calls: %d", budgeted.Calls())
	}
}

func TestBudgetedJudgeMakesNoCallForDisabledOrInvalidMaterial(t *testing.T) {
	ctx := context.Background()
	// Disabled: there is no Judge at all, so the count must stay zero rather than
	// being inferred from the absence of an error.
	disabled := &BudgetedJudge{Judge: nil, Budget: testBudget(4, 1000)}
	invocation := disabled.Invoke(ctx, materialWithAnswer(t, "答复"))
	if invocation.Outcome != CandidateError || invocation.Reason != ReasonDisabled {
		t.Fatalf("expected disabled, got %+v", invocation)
	}
	if disabled.Calls() != 0 {
		t.Fatalf("a disabled judge must not be invoked: %d", disabled.Calls())
	}

	// Contaminated material is refused before the call, so the judge is never
	// reached and nothing leaves the process.
	judge := &FakeJudge{}
	contaminated := &BudgetedJudge{Judge: judge, Budget: testBudget(4, 1000)}
	invocation = contaminated.Invoke(ctx, materialWithAnswer(t, "答复 "+SensitiveCanaryMarker))
	if invocation.Outcome != CandidateError || invocation.Reason != ReasonSensitiveInput {
		t.Fatalf("expected sensitive_input, got %+v", invocation)
	}
	if judge.Calls() != 0 {
		t.Fatalf("contaminated material must not reach the judge: %d", judge.Calls())
	}
}

func TestBudgetedJudgeReportsFailuresWithoutRetrying(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	failing := &FakeJudge{FallbackError: &JudgeError{Reason: ReasonCallError, Detail: "upstream"}}
	budgeted := &BudgetedJudge{Judge: failing, Budget: testBudget(4, 1000)}
	invocation := budgeted.Invoke(context.Background(), material)
	if invocation.Outcome != CandidateError || invocation.Reason != ReasonCallError {
		t.Fatalf("expected a closed call_error, got %+v", invocation)
	}
	if failing.Calls() != 1 {
		t.Fatalf("a failure must not be retried automatically: %d calls", failing.Calls())
	}

	invalid := &FakeJudge{FallbackError: &JudgeError{Reason: ReasonInvalidOutput}}
	if got := (&BudgetedJudge{Judge: invalid, Budget: testBudget(4, 1000)}).Invoke(context.Background(), material); got.Reason != ReasonInvalidOutput {
		t.Fatalf("invalid output must be reported as such: %+v", got)
	}
}

func TestBudgetedJudgeTerminatesAtTheRegisteredTimeout(t *testing.T) {
	judge := &countingJudge{block: make(chan struct{})}
	defer close(judge.block)
	budgeted := &BudgetedJudge{Judge: judge, Budget: testBudget(2, 20)}
	started := time.Now()
	invocation := budgeted.Invoke(context.Background(), materialWithAnswer(t, "答复"))
	elapsed := time.Since(started)
	if invocation.Outcome != CandidateError || invocation.Reason != ReasonTimeout {
		t.Fatalf("expected a closed timeout, got %+v", invocation)
	}
	if invocation.Response != nil {
		t.Fatal("a timed-out invocation has no prediction")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the timeout must bound the call, took %s", elapsed)
	}
	if judge.calls != 1 {
		t.Fatalf("no retry after a timeout: %d calls", judge.calls)
	}
}

func TestJudgeErrorKeepsAClosedReason(t *testing.T) {
	if ReasonOf(JudgeFailure(ReasonCallError, "boom")) != ReasonCallError {
		t.Fatal("a judge failure must carry its closed reason")
	}
	if JudgeReasonOf(nil) != ReasonCallError {
		t.Fatal("an unclassified judge error must fall back to call_error")
	}
}

func TestParseJudgeModeIsClosed(t *testing.T) {
	for _, value := range []string{"off", "fake", "real"} {
		mode, err := ParseJudgeMode(value)
		if err != nil || string(mode) != value {
			t.Fatalf("%s must be a valid mode: %v", value, err)
		}
	}
	for _, value := range []string{"", "Fake", "true", "on", "online"} {
		if _, err := ParseJudgeMode(value); err == nil {
			t.Fatalf("%q must be refused", value)
		}
	}
	if JudgeModeOff.Kind() != ScorerFake || JudgeModeFake.Kind() != ScorerFake || JudgeModeReal.Kind() != ScorerReal {
		t.Fatal("mode kinds must map onto the frozen scorer kinds")
	}
}

func reuseFixture(t *testing.T) (CalibrationReport, CalibrationIdentity) {
	t.Helper()
	identity := CalibrationIdentity{
		SchemaVersion: "1", CalibrationID: "calibration-1", Dimension: DimensionAnswerRelevanceValue,
		Dataset: "synthetic-operations@5", EvaluatorVersion: "2", Profile: "runtime-fake",
		ScorerKind: ScorerFake, ScorerDigest: "scorer-digest-1", MaterialSetDigest: "materials-1",
		AnnotationSetDigest: "annotations-1", SplitDigest: "split-1", RulesDigest: "rules-1",
		EnvironmentDigest: "environment-1", SourceDigest: "source-1",
	}
	identity.Digest = CalibrationIdentityDigest(identity)
	report := CalibrationReport{
		ReportSchemaVersion: CalibrationReportSchemaVersion,
		Identity:            identity,
		Scorer:              ScorerIdentity{SchemaVersion: "1", Kind: ScorerFake, Digest: "scorer-digest-1"},
		Acceptance:          Acceptance{Status: AcceptanceUsable},
	}
	return report, identity
}

func TestCalibrationReuseRequiresUsableMatchingIdentity(t *testing.T) {
	report, identity := reuseFixture(t)
	if check := CheckCalibrationReuse(report, JudgeModeFake, "scorer-digest-1", identity); !check.Allowed {
		t.Fatalf("a matching usable fake calibration must authorize the fake: %+v", check)
	}

	// A fake calibration can never authorize a real scorer.
	if check := CheckCalibrationReuse(report, JudgeModeReal, "scorer-digest-1", identity); check.Allowed ||
		check.Reason != ReasonCalibrationMismatch {
		t.Fatalf("fake must not authorize real: %+v", check)
	}

	// Not usable and not calibrated are distinct, closed reasons.
	unusable := report
	unusable.Acceptance.Status = AcceptanceUnusable
	if check := CheckCalibrationReuse(unusable, JudgeModeFake, "scorer-digest-1", identity); check.Allowed ||
		check.Reason != ReasonCalibrationUnusable {
		t.Fatalf("unusable: %+v", check)
	}
	notCalibrated := report
	notCalibrated.Acceptance.Status = AcceptanceNotCalibrated
	if check := CheckCalibrationReuse(notCalibrated, JudgeModeFake, "scorer-digest-1", identity); check.Allowed ||
		check.Reason != ReasonNotCalibrated {
		t.Fatalf("not calibrated: %+v", check)
	}

	// A different scorer is not the scorer that was calibrated.
	if check := CheckCalibrationReuse(report, JudgeModeFake, "other-scorer", identity); check.Allowed ||
		check.Reason != ReasonCalibrationMismatch {
		t.Fatalf("scorer mismatch: %+v", check)
	}

	// Any changed identity item invalidates the authorization and names itself.
	changed := identity
	changed.SplitDigest = "split-2"
	check := CheckCalibrationReuse(report, JudgeModeFake, "scorer-digest-1", changed)
	if check.Allowed || check.Reason != ReasonCalibrationMismatch {
		t.Fatalf("identity change: %+v", check)
	}
	if len(check.Invalidated) != 1 || check.Invalidated[0] != "split_digest" {
		t.Fatalf("the invalidated item must be named: %v", check.Invalidated)
	}
}

func TestLoadCalibrationReportRejectsUnsupportedEnvelope(t *testing.T) {
	report, _ := reuseFixture(t)
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	path := writeTemp(t, "calibration.json", string(body))
	if _, err := LoadCalibrationReport(path); err != nil {
		t.Fatalf("a supported envelope must load: %v", err)
	}
	stale := strings.Replace(string(body), `"report_schema_version":"1"`, `"report_schema_version":"0"`, 1)
	if _, err := LoadCalibrationReport(writeTemp(t, "stale.json", stale)); err == nil {
		t.Fatal("an unsupported calibration envelope must be refused before use")
	}
}
