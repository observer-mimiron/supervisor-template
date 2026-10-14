package semantic

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func ptr(value float64) *float64 { return &value }

func calibMaterial(t *testing.T, sampleID, groupID string) Material {
	t.Helper()
	spec := MaterialSpec{
		SchemaVersion: MaterialSchemaVersion, SampleID: sampleID, MaterialGroupID: groupID,
		DatasetName: "synthetic-operations", DatasetVersion: "5",
		CaseID: "case-" + groupID, CaseVersion: "1.0.0", VerdictDigest: "hard-" + sampleID,
		Dimension: DimensionAnswerRelevanceValue, IntentSource: IntentSourceReviewedBusinessGoal,
		TaskIntent: "查询符合沉睡客户条件的合成客户数量", SourceApproval: SourceApprovalSynthetic,
		Applicability: ApplicabilityApplicable,
	}
	material := Material{
		MaterialSpec: spec, PublicAnswer: "符合条件的合成客户共 12 位",
		Rubric: FrozenRubricIdentity(), Projection: FrozenProjectionIdentity(),
	}
	material.MaterialDigest = MaterialDigest(material)
	if err := material.Validate(); err != nil {
		t.Fatalf("fixture material %s: %v", sampleID, err)
	}
	return material
}

func calibBudget() BudgetIdentity {
	budget := BudgetIdentity{MaxCalls: 16, TimeoutMS: 5000, MaxInputBytes: MaxProjectionTotalBytes, MaxOutputBytes: 2048}
	budget.Digest = BudgetDigest(budget)
	return budget
}

// testGroups returns the registered group assignment used by most fixtures: six
// holdout groups and two debug groups.
func testGroups() ([]string, []string) {
	return []string{"g-debug-1", "g-debug-2"},
		[]string{"g-holdout-1", "g-holdout-2", "g-holdout-3", "g-holdout-4", "g-holdout-5", "g-holdout-6"}
}

func calibManifest(t *testing.T, verifiedControls map[BiasControlName]bool) RegisteredManifest {
	t.Helper()
	debug, holdout := testGroups()
	budget := calibBudget()
	scorer := ScorerIdentity{
		SchemaVersion: "1", Kind: ScorerFake, Provider: "local", Model: "fake-judge@1",
		ProviderRevisionStatus: ProviderRevisionUnverified,
		Parameters:             map[string]string{"temperature": "0"},
		PromptVersion:          "judge-prompt@1", PromptDigest: DigestText("judge prompt"),
		Rubric: FrozenRubricIdentity(), Projection: FrozenProjectionIdentity(),
		MarkerSet: FrozenSensitiveMarkerSetIdentity(), ReferenceSetDigest: ReferenceSetDigest(nil),
		BudgetDigest: budget.Digest,
	}
	scorer.Digest = ScorerDigest(scorer)
	controls := make([]BiasControl, 0, 4)
	for _, name := range RequiredBiasControls() {
		verified, present := verifiedControls[name]
		if !present {
			verified = true
		}
		controls = append(controls, BiasControl{Name: name, Verified: verified})
	}
	manifest := RegisteredManifest{
		SchemaVersion: RegisteredManifestSchemaVersion, ManifestID: "manifest-1",
		RegisteredAtUTC: time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC),
		Dimension:       DimensionAnswerRelevanceValue, Scorer: scorer, Budget: &budget,
		Splits: SplitPlan{
			SplitRule: SplitByMaterialGroupRule(), RepresentativeRule: RepresentativeRuleFirstEligibleBySampleID,
			DebugGroups: debug, HoldoutGroups: holdout,
		},
		Thresholds: Thresholds{
			MinIndependentTotal: 6,
			MinPerClass:         map[Label]int{LabelRelevant: 3, LabelIrrelevant: 3},
			MinAgreement:        ptr(0.6), MinAgreementLowerBound: ptr(0.3),
			MinKappa: ptr(0.2), MinKappaLowerBound: ptr(0.0),
			MinRecall:            map[Label]float64{LabelRelevant: 0.5, LabelIrrelevant: 0.5},
			MaxMissingRate:       ptr(0.25),
			BiasControlTolerance: ptr(0.1),
		},
		Bootstrap:    BootstrapRule{Resamples: 200, Seed: 7, Level: 0.95, MaxInvalidFraction: 0.25},
		BiasControls: controls,
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("fixture manifest: %v", err)
	}
	return manifest
}

// SplitByMaterialGroupRule exposes the frozen split rule to tests without
// duplicating the literal.
func SplitByMaterialGroupRule() string { return SplitRuleByMaterialGroup }

func calibTruth(t *testing.T, material Material, label Label) Annotation {
	t.Helper()
	annotation := Annotation{
		SchemaVersion: AnnotationSchemaVersion, AnnotationID: "annotation-" + material.SampleID,
		SampleID: material.SampleID, MaterialDigest: material.MaterialDigest,
		Dimension: material.Dimension, Label: label, AnnotatorID: "annotator-1",
		AnnotatedAtUTC: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC), Blind: true,
	}
	if err := annotation.Validate(); err != nil {
		t.Fatalf("fixture annotation: %v", err)
	}
	return annotation
}

func calibCandidate(t *testing.T, manifest RegisteredManifest, material Material, prediction *Label, outcome CandidateOutcome, reason ReasonCode) CalibrationCandidate {
	t.Helper()
	candidate := CalibrationCandidate{
		SchemaVersion: CalibrationCandidateSchemaVersion, Purpose: CalibrationCandidatePurpose,
		ManifestDigest: manifest.Digest(), ScorerDigest: manifest.Scorer.Digest, ScorerKind: manifest.Scorer.Kind,
		SampleID: material.SampleID, MaterialGroupID: material.MaterialGroupID,
		MaterialDigest: material.MaterialDigest, Dimension: material.Dimension,
		InvocationIndex: PrimaryInvocationIndex, Outcome: outcome, Prediction: prediction, ReasonCode: reason,
		RecordedAtUTC: time.Date(2026, 10, 9, 11, 0, 0, 0, time.UTC),
	}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("fixture candidate: %v", err)
	}
	return candidate
}

type calibrationFixture struct {
	Materials   []Material
	Annotations []Annotation
	Candidates  []CalibrationCandidate
	Manifest    RegisteredManifest
}

// correctFixture builds a registered, fully agreeing calibration: six holdout
// groups with a balanced two-class truth, one material per group, all bias
// controls verified.
func correctFixture(t *testing.T) calibrationFixture {
	t.Helper()
	manifest := calibManifest(t, nil)
	_, holdout := testGroups()
	debug, _ := testGroups()
	labels := []Label{LabelRelevant, LabelIrrelevant, LabelRelevant, LabelIrrelevant, LabelRelevant, LabelIrrelevant}
	fixture := calibrationFixture{Manifest: manifest}
	index := 0
	for _, group := range append(append([]string{}, debug...), holdout...) {
		label := labels[index%len(labels)]
		material := calibMaterial(t, "sample-"+group, group)
		prediction := label
		fixture.Materials = append(fixture.Materials, material)
		fixture.Annotations = append(fixture.Annotations, calibTruth(t, material, label))
		fixture.Candidates = append(fixture.Candidates, calibCandidate(t, manifest, material, &prediction, CandidatePrediction, ""))
		index++
	}
	return fixture
}

// rebindCandidates re-records the existing predictions under the current
// manifest digest. Changing any registered manifest value changes the manifest
// identity, so candidates recorded earlier no longer describe this measurement;
// a test that edits the registration must say so explicitly.
func (f *calibrationFixture) rebindCandidates() {
	digest := f.Manifest.Digest()
	for index := range f.Candidates {
		f.Candidates[index].ManifestDigest = digest
		f.Candidates[index].ScorerDigest = f.Manifest.Scorer.Digest
		f.Candidates[index].ScorerKind = f.Manifest.Scorer.Kind
	}
}

func (f calibrationFixture) build(t *testing.T) CalibrationReport {
	t.Helper()
	report, err := BuildCalibration(CalibrationInput{
		CalibrationID: "calibration-1", Materials: f.Materials, Annotations: f.Annotations,
		Candidates: f.Candidates, Manifest: f.Manifest, Dataset: "synthetic-operations@5",
		EvaluatorVersion: "2", Profile: "runtime-fake", SourceDigest: "source-digest-1",
		Now: time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("build calibration: %v", err)
	}
	return report
}

func splitByName(t *testing.T, report CalibrationReport, name string) SplitReport {
	t.Helper()
	for _, split := range report.Splits {
		if split.Name == name {
			return split
		}
	}
	t.Fatalf("split %s is missing from the report", name)
	return SplitReport{}
}

func TestCalibrationIsUsableOnTheRegisteredCorrectFixture(t *testing.T) {
	report := correctFixture(t).build(t)
	if report.Acceptance.Status != AcceptanceUsable {
		t.Fatalf("expected usable, got %s reasons=%v conditions=%+v",
			report.Acceptance.Status, report.Acceptance.Reasons, report.Acceptance.Conditions)
	}
	if len(report.Acceptance.Reasons) != 0 {
		t.Fatalf("a usable calibration carries no failure reasons: %v", report.Acceptance.Reasons)
	}
	if report.ReportSchemaVersion != CalibrationReportSchemaVersion {
		t.Fatalf("report schema: %s", report.ReportSchemaVersion)
	}
	if report.Scorer.Kind != ScorerFake || report.Identity.ScorerKind != ScorerFake {
		t.Fatal("the report must record the fake/real provenance it was computed from")
	}
	holdout := splitByName(t, report, SplitHoldout)
	if holdout.Groups != 6 || holdout.Materials != 6 {
		t.Fatalf("holdout set: groups=%d materials=%d", holdout.Groups, holdout.Materials)
	}
	// The acceptance decision is taken on the holdout primary block only.
	if !strings.Contains(report.Acceptance.DecidedOn, SplitHoldout) {
		t.Fatalf("acceptance must be decided on holdout, got %q", report.Acceptance.DecidedOn)
	}
	if report.SampleFlow.Applicable != 8 || report.SampleFlow.Paired != 8 {
		t.Fatalf("sample flow: %+v", report.SampleFlow)
	}
	if report.SampleFlow.MissingRate == nil || *report.SampleFlow.MissingRate != 0 {
		t.Fatalf("missing rate: %+v", report.SampleFlow)
	}
	// Every registered parameter is reported back as a satisfied condition.
	names := map[string]bool{}
	for _, condition := range report.Acceptance.Conditions {
		names[condition.Name] = true
		if !condition.Satisfied {
			t.Fatalf("condition %s should be satisfied: %+v", condition.Name, condition)
		}
	}
	for _, required := range []string{"independent_groups", "agreement", "kappa", "missing_rate",
		"bias_control:position", "bias_control:length", "bias_control:self_preference", "bias_control:prompt_injection"} {
		if !names[required] {
			t.Fatalf("condition %s is missing from the report: %v", required, names)
		}
	}
}

func TestCalibrationIsUnusableWhenAnyBiasControlIsMissingOrUnverified(t *testing.T) {
	for _, name := range RequiredBiasControls() {
		t.Run(string(name), func(t *testing.T) {
			fixture := correctFixture(t)
			// Remove the control entirely: an absent control is not a verified one.
			controls := make([]BiasControl, 0, len(fixture.Manifest.BiasControls))
			for _, control := range fixture.Manifest.BiasControls {
				if control.Name == name {
					continue
				}
				controls = append(controls, control)
			}
			fixture.Manifest.BiasControls = controls
			fixture.rebindCandidates()
			report := fixture.build(t)
			if report.Acceptance.Status != AcceptanceUnusable {
				t.Fatalf("a missing %s control must not be usable, got %s", name, report.Acceptance.Status)
			}
			if !containsReason(report.Acceptance.Reasons, AcceptanceReasonBiasControlUnverified) {
				t.Fatalf("expected bias_control_unverified, got %v", report.Acceptance.Reasons)
			}

			unverified := correctFixture(t)
			for index := range unverified.Manifest.BiasControls {
				if unverified.Manifest.BiasControls[index].Name == name {
					unverified.Manifest.BiasControls[index].Verified = false
				}
			}
			unverified.rebindCandidates()
			report = unverified.build(t)
			if report.Acceptance.Status == AcceptanceUsable {
				t.Fatalf("an unverified %s control must not be usable", name)
			}
		})
	}
}

func containsReason(reasons []AcceptanceReason, want AcceptanceReason) bool {
	for _, reason := range reasons {
		if reason == want {
			return true
		}
	}
	return false
}

func TestCalibrationIsUnusableForConstantPredictionEvenWithHighAgreement(t *testing.T) {
	fixture := correctFixture(t)
	relevant := LabelRelevant
	for index := range fixture.Candidates {
		fixture.Candidates[index].Prediction = &relevant
	}
	report := fixture.build(t)
	if report.Acceptance.Status == AcceptanceUsable {
		t.Fatal("a constant scorer must never be usable, whatever its agreement")
	}
	if !containsReason(report.Acceptance.Reasons, AcceptanceReasonConstantPrediction) {
		t.Fatalf("expected constant_prediction, got %v", report.Acceptance.Reasons)
	}
	// The numbers stay real: only the policy verdict is negative.
	holdout := splitByName(t, report, SplitHoldout)
	if holdout.Primary.Kappa.Status != EstimateEstimated {
		t.Fatalf("kappa has a valid denominator here and must be reported: %+v", holdout.Primary.Kappa)
	}
	if *holdout.Primary.Agreement.Value != 0.5 {
		t.Fatalf("agreement against balanced labels: %v", *holdout.Primary.Agreement.Value)
	}
}

func TestCalibrationIsUnusableForSingleClassHumanLabels(t *testing.T) {
	fixture := correctFixture(t)
	for index := range fixture.Annotations {
		fixture.Annotations[index].Label = LabelRelevant
	}
	report := fixture.build(t)
	if report.Acceptance.Status == AcceptanceUsable {
		t.Fatal("single-class human labels must never be usable")
	}
	if !containsReason(report.Acceptance.Reasons, AcceptanceReasonSingleClassLabels) {
		t.Fatalf("expected single_class_human_labels, got %v", report.Acceptance.Reasons)
	}
}

func TestCalibrationIsUnusableWithoutHoldoutEvidence(t *testing.T) {
	// No holdout predictions at all: the registered thresholds have nothing to
	// apply to, and debug evidence must not authorize the scorer.
	fixture := correctFixture(t)
	_, holdout := testGroups()
	keep := map[string]bool{}
	for _, group := range holdout {
		keep[group] = true
	}
	candidates := make([]CalibrationCandidate, 0, len(fixture.Candidates))
	for _, candidate := range fixture.Candidates {
		if !keep[candidate.MaterialGroupID] {
			candidates = append(candidates, candidate)
		}
	}
	fixture.Candidates = candidates
	report := fixture.build(t)
	if report.Acceptance.Status != AcceptanceNotCalibrated {
		t.Fatalf("expected not_calibrated without holdout pairs, got %s", report.Acceptance.Status)
	}
	if !containsReason(report.Acceptance.Reasons, AcceptanceReasonNoPairs) {
		t.Fatalf("expected no_paired_predictions, got %v", report.Acceptance.Reasons)
	}
}

// Repeating a source variant must not buy statistical power: the primary
// statistics count one representative per independent group.
func TestCalibrationDoesNotLetDuplicateVariantsInflateTheSample(t *testing.T) {
	fixture := correctFixture(t)
	manifest := fixture.Manifest
	extraMaterials := make([]Material, 0)
	extraAnnotations := make([]Annotation, 0)
	extraCandidates := make([]CalibrationCandidate, 0)
	for _, material := range fixture.Materials {
		// A second variant of the same source group, with the same label.
		variant := calibMaterial(t, material.SampleID+"-variant", material.MaterialGroupID)
		prediction := LabelRelevant
		extraMaterials = append(extraMaterials, variant)
		extraAnnotations = append(extraAnnotations, calibTruth(t, variant, LabelRelevant))
		extraCandidates = append(extraCandidates, calibCandidate(t, manifest, variant, &prediction, CandidatePrediction, ""))
	}
	fixture.Materials = append(fixture.Materials, extraMaterials...)
	fixture.Annotations = append(fixture.Annotations, extraAnnotations...)
	fixture.Candidates = append(fixture.Candidates, extraCandidates...)
	report := fixture.build(t)

	holdout := splitByName(t, report, SplitHoldout)
	if holdout.Groups != 6 {
		t.Fatalf("adding variants must not add independent groups: %d", holdout.Groups)
	}
	if holdout.Primary.Pairs != 6 {
		t.Fatalf("the primary statistic uses one representative per group, got %d pairs", holdout.Primary.Pairs)
	}
	if holdout.FullSet.Pairs != 12 {
		t.Fatalf("the full-material point estimate still sees every material: %d", holdout.FullSet.Pairs)
	}
	if len(holdout.RepresentativeSampleIDs) != 6 {
		t.Fatalf("representative selection must be reported: %v", holdout.RepresentativeSampleIDs)
	}
}

func TestCalibrationCountsErrorsAndSkipsInTheDenominator(t *testing.T) {
	fixture := correctFixture(t)
	// Three planned invocations fail: they must stay in the denominator instead
	// of being dropped so only the successes are scored.
	for _, index := range []int{2, 4, 6} {
		fixture.Candidates[index].Outcome = CandidateError
		fixture.Candidates[index].Prediction = nil
		fixture.Candidates[index].ReasonCode = ReasonTimeout
	}
	report := fixture.build(t)
	if report.SampleFlow.Errors != 3 {
		t.Fatalf("errors must be counted: %+v", report.SampleFlow)
	}
	if report.SampleFlow.Paired != 5 {
		t.Fatalf("paired predictions: %+v", report.SampleFlow)
	}
	if report.SampleFlow.MissingRate == nil || *report.SampleFlow.MissingRate <= 0.25 {
		t.Fatalf("missing rate must exceed the registered maximum: %+v", report.SampleFlow)
	}
	if report.Acceptance.Status == AcceptanceUsable {
		t.Fatal("a run beyond the registered missing rate must not be usable")
	}
	if !containsReason(report.Acceptance.Reasons, AcceptanceReasonMissingRateAbove) {
		t.Fatalf("expected missing_rate_above_threshold, got %v", report.Acceptance.Reasons)
	}
	if len(report.InvocationRecords) != len(fixture.Candidates) {
		t.Fatalf("every planned invocation must be recorded: %d of %d",
			len(report.InvocationRecords), len(fixture.Candidates))
	}
	errors := 0
	for _, record := range report.InvocationRecords {
		if record.Outcome == CandidateError {
			errors++
		}
	}
	if errors != 3 {
		t.Fatalf("failed invocations must not disappear from the per-round evidence: %d", errors)
	}
}

func TestCalibrationSeparatesAllLabelAndApplicableStatistics(t *testing.T) {
	fixture := correctFixture(t)
	_, holdout := testGroups()
	notApplicable := calibMaterial(t, "sample-not-applicable", holdout[0])
	notApplicable.Applicability = ApplicabilityNotApplicable
	notApplicable.NotApplicableReason = NotApplicableReasonNoAnswerRequired
	notApplicable.PublicAnswer = ""
	notApplicable.MaterialDigest = MaterialDigest(notApplicable)
	if err := notApplicable.Validate(); err != nil {
		t.Fatal(err)
	}
	fixture.Materials = append(fixture.Materials, notApplicable)
	fixture.Annotations = append(fixture.Annotations, calibTruth(t, notApplicable, LabelNotApplicable))
	// The sample contract requires no answer, so there is nothing to judge: a
	// planned invocation for it is recorded as a skip, not as a pair.
	skip := calibCandidate(t, fixture.Manifest, notApplicable, nil, CandidateSkip, ReasonMaterialMissing)
	fixture.Candidates = append(fixture.Candidates, skip)
	report := fixture.build(t)

	if report.SampleFlow.NotApplicable != 1 {
		t.Fatalf("not-applicable samples must be counted separately: %+v", report.SampleFlow)
	}
	// A not-applicable sample must not inflate the applicable denominator.
	if report.SampleFlow.Applicable != 8 {
		t.Fatalf("applicable count: %+v", report.SampleFlow)
	}
	if len(splitByName(t, report, "applicable").FullSet.HumanCounts) != 3 {
		t.Fatal("the applicable view must still expose the full frozen label set")
	}
	if others := splitByName(t, report, "applicable").FullSet.Pairs; others != 8 {
		t.Fatalf("the applicable denominator must not change: %d", others)
	}
	if report.SampleFlow.Paired != 8 {
		t.Fatalf("a skip is not a pair: %+v", report.SampleFlow)
	}
	// The skip stays visible in the per-invocation evidence.
	found := false
	for _, record := range report.InvocationRecords {
		if record.SampleID == notApplicable.SampleID {
			found = record.Outcome == CandidateSkip && record.ReasonCode == ReasonMaterialMissing
		}
	}
	if !found {
		t.Fatal("the skipped not-applicable invocation must remain in the evidence")
	}
}

// A Judge that answers not_applicable on an applicable sample is wrong, and that
// wrong answer must stay in the applicable matrix instead of being dropped as
// "not applicable".
func TestJudgeMispredictingNotApplicableRemainsAnError(t *testing.T) {
	fixture := correctFixture(t)
	_, holdout := testGroups()
	mispredicted := LabelNotApplicable
	for index := range fixture.Candidates {
		if fixture.Candidates[index].MaterialGroupID == holdout[0] {
			fixture.Candidates[index].Prediction = &mispredicted
		}
	}
	report := fixture.build(t)
	holdoutReport := splitByName(t, report, SplitHoldout)
	if holdoutReport.FullSet.PredictionCounts[LabelNotApplicable] != 1 {
		t.Fatalf("the misprediction must remain in the matrix: %+v", holdoutReport.FullSet.PredictionCounts)
	}
	if holdoutReport.Primary.Pairs != 6 {
		t.Fatalf("the row is not silently deleted: %d pairs", holdoutReport.Primary.Pairs)
	}
	if *holdoutReport.Primary.Agreement.Value == 1 {
		t.Fatal("a mispredicted not_applicable must lower agreement")
	}
	if holdoutReport.Primary.Recall[LabelRelevant].Value == nil || *holdoutReport.Primary.Recall[LabelRelevant].Value != 2.0/3.0 {
		t.Fatalf("per-class recall must reflect the misprediction: %+v", holdoutReport.Primary.Recall[LabelRelevant])
	}
	// The permissive registered thresholds tolerate one error in six; that is the
	// registrant's call. What must hold is that the error reaches the verdict:
	// under a registered perfect-recall requirement the same predictions fail.
	fixture.Manifest.Thresholds.MinRecall = map[Label]float64{LabelRelevant: 1, LabelIrrelevant: 1}
	fixture.rebindCandidates()
	strict := fixture.build(t)
	if strict.Acceptance.Status == AcceptanceUsable {
		t.Fatal("a misreported not_applicable must fail a registered perfect-recall requirement")
	}
	if !containsReason(strict.Acceptance.Reasons, AcceptanceReasonRecallBelow) {
		t.Fatalf("expected recall_below_threshold, got %v", strict.Acceptance.Reasons)
	}
}

func TestCalibrationRejectsUnregisteredAndOverlappingGroups(t *testing.T) {
	// A group in both splits would leak between debug and holdout.
	overlap := calibManifest(t, nil)
	overlap.Splits.HoldoutGroups = append(overlap.Splits.HoldoutGroups, overlap.Splits.DebugGroups[0])
	if err := overlap.Splits.Validate(); err == nil {
		t.Fatal("a group in both splits must be rejected")
	}

	// The 13 behavior Cases are not the split unit: a plan that splits by Case
	// is refused, while two materials that share a Case but not a group must be
	// allowed to land in different splits.
	byCase := calibManifest(t, nil)
	byCase.Splits.SplitRule = "by_case"
	if err := byCase.Splits.Validate(); err == nil {
		t.Fatal("splitting by Case must be refused")
	}

	fixture := correctFixture(t)
	unregistered := calibMaterial(t, "sample-unregistered", "g-not-registered")
	prediction := LabelRelevant
	fixture.Materials = append(fixture.Materials, unregistered)
	fixture.Annotations = append(fixture.Annotations, calibTruth(t, unregistered, LabelRelevant))
	fixture.Candidates = append(fixture.Candidates,
		calibCandidate(t, fixture.Manifest, unregistered, &prediction, CandidatePrediction, ""))
	report, err := BuildCalibration(CalibrationInput{
		CalibrationID: "calibration-1", Materials: fixture.Materials, Annotations: fixture.Annotations,
		Candidates: fixture.Candidates, Manifest: fixture.Manifest, Dataset: "synthetic-operations@5",
		EvaluatorVersion: "2", Profile: "runtime-fake", Now: time.Now().UTC(),
	})
	if err == nil {
		t.Fatalf("a material outside the registered split plan must be refused, got %+v", report.Acceptance)
	}
}

func TestCalibrationExcludesUnresolvedDisagreementFromTruth(t *testing.T) {
	fixture := correctFixture(t)
	_, holdout := testGroups()
	// Two annotators disagree on one holdout material and nobody reviewed it.
	disputed := fixture.Materials[2]
	second := calibTruth(t, disputed, LabelIrrelevant)
	second.AnnotationID = "annotation-" + disputed.SampleID + "-b"
	second.AnnotatorID = "annotator-2"
	fixture.Annotations = append(fixture.Annotations, second)
	report := fixture.build(t)

	if report.SampleFlow.Disagreements != 1 {
		t.Fatalf("the disagreement must be retained and reported: %+v", report.SampleFlow)
	}
	holdoutReport := splitByName(t, report, SplitHoldout)
	if holdoutReport.Groups != 5 {
		t.Fatalf("an unresolved sample produces no truth, so it is not paired: groups=%d", holdoutReport.Groups)
	}
	if report.Acceptance.Status == AcceptanceUsable {
		t.Fatal("losing the registered independent total must not stay usable")
	}
	if !containsReason(report.Acceptance.Reasons, AcceptanceReasonInsufficientGroups) {
		t.Fatalf("expected insufficient_independent_groups, got %v", report.Acceptance.Reasons)
	}
	_ = holdout
}

func TestCalibrationRejectsCandidateProvenanceMismatch(t *testing.T) {
	cases := map[string]func(*CalibrationCandidate){
		"manifest":       func(c *CalibrationCandidate) { c.ManifestDigest = "another-manifest" },
		"scorer":         func(c *CalibrationCandidate) { c.ScorerDigest = "another-scorer" },
		"scorer kind":    func(c *CalibrationCandidate) { c.ScorerKind = ScorerReal },
		"dimension":      func(c *CalibrationCandidate) { c.Dimension = "factual_correctness@1" },
		"material group": func(c *CalibrationCandidate) { c.MaterialGroupID = "g-elsewhere" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := correctFixture(t)
			mutate(&fixture.Candidates[0])
			_, err := BuildCalibration(CalibrationInput{
				CalibrationID: "calibration-1", Materials: fixture.Materials, Annotations: fixture.Annotations,
				Candidates: fixture.Candidates, Manifest: fixture.Manifest, Dataset: "synthetic-operations@5",
				EvaluatorVersion: "2", Profile: "runtime-fake", Now: time.Now().UTC(),
			})
			if err == nil {
				t.Fatalf("a candidate with a different %s identity must be refused", name)
			}
		})
	}
	// Unknown material references are refused too: a prediction for a sample
	// outside the material set cannot be paired with anything.
	fixture := correctFixture(t)
	unknown := calibCandidate(t, fixture.Manifest, fixture.Materials[0], nil, CandidateError, ReasonCallError)
	unknown.SampleID = "sample-that-does-not-exist"
	if err := unknown.Validate(); err != nil {
		t.Fatal(err)
	}
	fixture.Candidates = append(fixture.Candidates, unknown)
	if _, err := BuildCalibration(CalibrationInput{
		CalibrationID: "calibration-1", Materials: fixture.Materials, Annotations: fixture.Annotations,
		Candidates: fixture.Candidates, Manifest: fixture.Manifest, Dataset: "synthetic-operations@5",
		EvaluatorVersion: "2", Profile: "runtime-fake", Now: time.Now().UTC(),
	}); err == nil {
		t.Fatal("a prediction for an unknown material must be refused")
	}
}

func TestIdentityInvalidationNamesEveryChangedItem(t *testing.T) {
	base := CalibrationIdentity{
		SchemaVersion: "1", CalibrationID: "calibration-1", Dimension: DimensionAnswerRelevanceValue,
		Dataset: "synthetic-operations@5", EvaluatorVersion: "2", Profile: "runtime-fake",
		ScorerKind: ScorerFake, ScorerDigest: "scorer-1", MaterialSetDigest: "materials-1",
		AnnotationSetDigest: "annotations-1", SplitDigest: "split-1", RulesDigest: "rules-1",
		EnvironmentDigest: "environment-1", SourceDigest: "source-1",
	}
	base.Digest = CalibrationIdentityDigest(base)
	if invalidated := InvalidatedFields(base, base); len(invalidated) != 0 {
		t.Fatalf("an identical identity must not invalidate anything: %v", invalidated)
	}
	cases := map[string]func(*CalibrationIdentity){
		"scorer_digest":         func(i *CalibrationIdentity) { i.ScorerDigest = "scorer-2" },
		"material_set_digest":   func(i *CalibrationIdentity) { i.MaterialSetDigest = "materials-2" },
		"annotation_set_digest": func(i *CalibrationIdentity) { i.AnnotationSetDigest = "annotations-2" },
		"split_digest":          func(i *CalibrationIdentity) { i.SplitDigest = "split-2" },
		"rules_digest":          func(i *CalibrationIdentity) { i.RulesDigest = "rules-2" },
		"environment_digest":    func(i *CalibrationIdentity) { i.EnvironmentDigest = "environment-2" },
		"profile":               func(i *CalibrationIdentity) { i.Profile = "runtime-real" },
		"dataset":               func(i *CalibrationIdentity) { i.Dataset = "synthetic-operations@6" },
		"evaluator_version":     func(i *CalibrationIdentity) { i.EvaluatorVersion = "3" },
		"scorer_kind":           func(i *CalibrationIdentity) { i.ScorerKind = ScorerReal },
		"source_digest":         func(i *CalibrationIdentity) { i.SourceDigest = "source-2" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			invalidated := InvalidatedFields(base, changed)
			if len(invalidated) != 1 || invalidated[0] != name {
				t.Fatalf("expected exactly [%s], got %v", name, invalidated)
			}
			// The identity digest must move with the identity, so a stale
			// calibration cannot be replayed under a new one.
			if changed.Digest == base.Digest {
				changed.Digest = CalibrationIdentityDigest(changed)
			}
			if CalibrationIdentityDigest(changed) == CalibrationIdentityDigest(base) {
				t.Fatal("a changed identity must change its digest")
			}
		})
	}
}

func TestSplitAndRulesDigestsTrackTheirContent(t *testing.T) {
	manifest := calibManifest(t, nil)
	base := SplitDigest(manifest.Splits)
	reordered := manifest.Splits
	reordered.HoldoutGroups = append([]string(nil), manifest.Splits.HoldoutGroups...)
	reordered.HoldoutGroups[0], reordered.HoldoutGroups[1] = reordered.HoldoutGroups[1], reordered.HoldoutGroups[0]
	if SplitDigest(reordered) != base {
		t.Fatal("group order is not part of the split identity")
	}
	moved := manifest.Splits
	moved.DebugGroups = append(append([]string(nil), manifest.Splits.DebugGroups...), "g-holdout-1")
	moved.HoldoutGroups = moved.HoldoutGroups[1:]
	if SplitDigest(moved) == base {
		t.Fatal("moving a group between splits must change the split identity")
	}

	rules := RulesDigest(manifest)
	changed := manifest
	changed.Thresholds.MinKappa = ptr(0.9)
	if RulesDigest(changed) == rules {
		t.Fatal("changing a registered threshold must change the rules identity")
	}
	manifested := manifest
	manifested.RegisteredAtUTC = manifest.RegisteredAtUTC.Add(time.Hour)
	if manifested.Digest() == manifest.Digest() {
		t.Fatal("the registration time is part of the manifest identity")
	}
}

func TestThresholdsRequireEveryRegisteredValue(t *testing.T) {
	manifest := calibManifest(t, nil)
	cases := map[string]func(*Thresholds){
		"min_independent_total":     func(t *Thresholds) { t.MinIndependentTotal = 0 },
		"min_agreement":             func(t *Thresholds) { t.MinAgreement = nil },
		"min_agreement_lower_bound": func(t *Thresholds) { t.MinAgreementLowerBound = nil },
		"min_kappa":                 func(t *Thresholds) { t.MinKappa = nil },
		"min_kappa_lower_bound":     func(t *Thresholds) { t.MinKappaLowerBound = nil },
		"min_per_class":             func(t *Thresholds) { t.MinPerClass = nil },
		"min_recall":                func(t *Thresholds) { t.MinRecall = nil },
		"max_missing_rate":          func(t *Thresholds) { t.MaxMissingRate = nil },
		"bias_control_tolerance":    func(t *Thresholds) { t.BiasControlTolerance = nil },
		"not a number":              func(t *Thresholds) { t.MinKappa = ptr(math_NaN()) },
		"out of range":              func(t *Thresholds) { t.MaxMissingRate = ptr(1.5) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := manifest
			mutate(&changed.Thresholds)
			// There is no default pass line: a missing or non-finite parameter is
			// a configuration error.
			if err := changed.Thresholds.Validate(); err == nil {
				t.Fatalf("expected %s to be refused", name)
			}
		})
	}
}

func math_NaN() float64 {
	var zero float64
	return zero / zero * 1
}

func TestCalibrationIsDeterministicAndSerializesWithoutNaN(t *testing.T) {
	first := correctFixture(t).build(t)
	second := correctFixture(t).build(t)
	left, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(left) != string(right) {
		t.Fatal("the same inputs must produce the same report")
	}
	if strings.Contains(string(left), "NaN") || strings.Contains(string(left), "Inf") {
		t.Fatal("a report must never contain NaN or Inf")
	}
	if !strings.Contains(string(left), `"status":"usable"`) {
		t.Fatalf("expected the acceptance verdict in the report: %s", truncate(string(left), 400))
	}
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func TestHumanReliabilityIsReportedSeparatelyFromJudgeCalibration(t *testing.T) {
	// Single annotator: reliability is unverified even though the Judge agrees
	// perfectly with that one person.
	single := correctFixture(t).build(t)
	if single.HumanReliability.Status != EstimateNotEstimable ||
		single.HumanReliability.Reason != HumanReliabilitySinglePerson {
		t.Fatalf("a single annotator cannot establish reliability: %+v", single.HumanReliability)
	}
	if single.Acceptance.Status != AcceptanceUsable {
		t.Fatal("single-person labeling does not by itself block usability; it is reported as unverified")
	}

	// Two independent blind annotators on the same materials, disagreeing on
	// some: reliability is computed on the pre-adjudication labels.
	fixture := correctFixture(t)
	second := make([]Annotation, 0, len(fixture.Annotations))
	for index, annotation := range fixture.Annotations {
		extra := annotation
		extra.AnnotationID = annotation.AnnotationID + "-b"
		extra.AnnotatorID = "annotator-2"
		if index%2 == 0 {
			extra.Label = LabelIrrelevant
		}
		second = append(second, extra)
	}
	// The disagreements are adjudicated, so truth still exists.
	reviews := make([]Review, 0)
	for index, annotation := range fixture.Annotations {
		if index%2 != 0 {
			continue
		}
		reviews = append(reviews, Review{
			SchemaVersion: ReviewSchemaVersion, ReviewID: "review-" + annotation.AnnotationID,
			SampleID: annotation.SampleID, MaterialDigest: annotation.MaterialDigest,
			Dimension: annotation.Dimension, ReviewerID: "reviewer-1",
			ReviewedAtUTC: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
			AdoptedLabel:  annotation.Label, Resolved: true,
			SourceAnnotationIDs: []string{annotation.AnnotationID, annotation.AnnotationID + "-b"},
		})
	}
	fixture.Annotations = append(fixture.Annotations, second...)
	report, err := BuildCalibration(CalibrationInput{
		CalibrationID: "calibration-1", Materials: fixture.Materials, Annotations: fixture.Annotations,
		Reviews: reviews, Candidates: fixture.Candidates, Manifest: fixture.Manifest,
		Dataset: "synthetic-operations@5", EvaluatorVersion: "2", Profile: "runtime-fake",
		Now: time.Date(2026, 10, 9, 14, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if report.HumanReliability.Status != EstimateEstimated {
		t.Fatalf("two independent blind annotators must produce reliability: %+v", report.HumanReliability)
	}
	if report.HumanReliability.Reason != HumanReliabilityTwoIndependent {
		t.Fatalf("reason: %+v", report.HumanReliability)
	}
	if report.HumanReliability.Paired != len(fixture.Annotations)/2 {
		t.Fatalf("paired count: %+v", report.HumanReliability)
	}
	// The Judge agreed with annotator-1 everywhere, so its own numbers are
	// perfect while human reliability is 0.5. The two must not be conflated.
	if hit := report.HumanReliability.Agreement; hit.Value == nil || *hit.Value != 0.5 {
		t.Fatalf("human-vs-human agreement must reflect the disagreement: %+v", hit)
	}
	holdout := splitByName(t, report, SplitHoldout)
	if holdout.Primary.Agreement.Value == nil || *holdout.Primary.Agreement.Value != 1 {
		t.Fatalf("human-vs-Judge agreement is a separate number: %+v", holdout.Primary.Agreement)
	}
	if report.HumanReliability.Kappa.Status == EstimateNotEstimable {
		t.Fatalf("kappa has a valid denominator here: %+v", report.HumanReliability.Kappa)
	}
}
