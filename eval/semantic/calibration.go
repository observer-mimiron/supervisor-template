package semantic

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Offline calibration is a replay: it reads local materials, resolved human
// labels and locally recorded predictions, computes the registered statistics,
// and decides whether that scorer's labels may be relied on. It never calls a
// model, and it never lets a model's opinion become a human label.
//
// The report deliberately separates the two things a single agreement number
// would blur: how well the scorer agreed, and whether the evidence is strong
// enough to rely on it. Thresholds are registered before holdout validation;
// they are never tuned by the same run that reports them.

// Schema and identity constants for the offline replay.
const (
	// CalibrationReportSchemaVersion is the envelope version of the report.
	CalibrationReportSchemaVersion = "1"
	// RegisteredManifestSchemaVersion is the envelope version of a manifest.
	RegisteredManifestSchemaVersion = "1"
	// CalibrationCandidateSchemaVersion is the envelope version of a candidate record.
	CalibrationCandidateSchemaVersion = "1"
	// CalibrationCandidatePurpose marks prediction records that are not truth.
	CalibrationCandidatePurpose = "calibration_candidate"
	// SplitRuleByMaterialGroup is the frozen split rule: splits are over
	// independent material groups, never over the behavior Cases.
	SplitRuleByMaterialGroup = "by_material_group"
	// RepresentativeRuleFirstEligibleBySampleID is the frozen representative
	// rule, fixed before any label or prediction is inspected.
	RepresentativeRuleFirstEligibleBySampleID = "first_eligible_by_sample_id"
	// PrimaryInvocationIndex is the invocation the primary statistics use. Extra
	// invocation indices are reported separately and never pooled to inflate n.
	PrimaryInvocationIndex = 0
	// SplitDebug and SplitHoldout name the two registered splits.
	SplitDebug   = "debug"
	SplitHoldout = "holdout"
)

// CandidateOutcome is the closed state of one planned invocation.
type CandidateOutcome string

// Closed candidate outcomes. An error or a skip is not a quality label; it stays
// in the denominator.
const (
	CandidatePrediction CandidateOutcome = "prediction"
	CandidateError      CandidateOutcome = "error"
	CandidateSkip       CandidateOutcome = "skip"
)

// BiasControlName is a frozen bias control that must be verified before a
// calibration is trusted.
type BiasControlName string

// Closed bias control names.
const (
	BiasControlPosition        BiasControlName = "position"
	BiasControlLength          BiasControlName = "length"
	BiasControlSelfPreference  BiasControlName = "self_preference"
	BiasControlPromptInjection BiasControlName = "prompt_injection"
)

// RequiredBiasControls is the frozen control set. A missing or unverified
// control makes a calibration unusable, because the corresponding failure mode
// was never ruled out.
func RequiredBiasControls() []BiasControlName {
	return []BiasControlName{
		BiasControlPosition, BiasControlLength, BiasControlSelfPreference, BiasControlPromptInjection,
	}
}

// BiasControl records whether one frozen control was actually verified, with a
// bounded pointer to the evidence. Offline tests must not set it true.
type BiasControl struct {
	Name     BiasControlName `json:"name"`
	Verified bool            `json:"verified"`
	Evidence string          `json:"evidence,omitempty"`
}

// SplitPlan is the pre-registered debug/holdout division over material groups.
type SplitPlan struct {
	SplitRule          string   `json:"split_rule"`
	RepresentativeRule string   `json:"representative_rule"`
	DebugGroups        []string `json:"debug_groups"`
	HoldoutGroups      []string `json:"holdout_groups"`
}

// Thresholds are the pre-registered acceptance parameters. Every value is a
// pointer so an omitted field is a configuration error rather than a silent
// zero that would accept anything.
type Thresholds struct {
	MinIndependentTotal    int               `json:"min_independent_total"`
	MinPerClass            map[Label]int     `json:"min_per_class"`
	MinAgreement           *float64          `json:"min_agreement"`
	MinAgreementLowerBound *float64          `json:"min_agreement_lower_bound"`
	MinKappa               *float64          `json:"min_kappa"`
	MinKappaLowerBound     *float64          `json:"min_kappa_lower_bound"`
	MinRecall              map[Label]float64 `json:"min_recall"`
	MaxMissingRate         *float64          `json:"max_missing_rate"`
	BiasControlTolerance   *float64          `json:"bias_control_tolerance"`
}

// ApprovedMaterial is one sample the manifest approved for collection.
type ApprovedMaterial struct {
	SampleID        string `json:"sample_id"`
	MaterialGroupID string `json:"material_group_id"`
	MaterialDigest  string `json:"material_digest"`
}

// RegisteredManifest is the pre-registered sampling/split/scorer/rules document.
// It is used both to authorize candidate collection and to replay calibration
// offline, so the two cannot drift apart.
type RegisteredManifest struct {
	SchemaVersion     string             `json:"schema_version"`
	ManifestID        string             `json:"manifest_id"`
	RegisteredAtUTC   time.Time          `json:"registered_at_utc"`
	Dimension         Dimension          `json:"dimension"`
	Scorer            ScorerIdentity     `json:"scorer"`
	Budget            *BudgetIdentity    `json:"budget,omitempty"`
	ApprovedMaterials []ApprovedMaterial `json:"approved_materials,omitempty"`
	Splits            SplitPlan          `json:"splits"`
	Thresholds        Thresholds         `json:"thresholds"`
	Bootstrap         BootstrapRule      `json:"bootstrap"`
	BiasControls      []BiasControl      `json:"bias_controls"`
}

// CalibrationCandidate is one recorded invocation of one scorer on one material.
//
// It is not a human label and not a formal semantic result. It exists so the
// first calibration has evidence to replay, and it can only be consumed by the
// offline replay.
type CalibrationCandidate struct {
	SchemaVersion   string           `json:"schema_version"`
	Purpose         string           `json:"purpose"`
	ManifestDigest  string           `json:"manifest_digest"`
	ScorerDigest    string           `json:"scorer_digest"`
	ScorerKind      ScorerKind       `json:"scorer_kind"`
	SampleID        string           `json:"sample_id"`
	MaterialGroupID string           `json:"material_group_id"`
	MaterialDigest  string           `json:"material_digest"`
	Dimension       Dimension        `json:"dimension"`
	InvocationIndex int              `json:"invocation_index"`
	Outcome         CandidateOutcome `json:"outcome"`
	Prediction      *Label           `json:"prediction"`
	ReasonCode      ReasonCode       `json:"reason_code,omitempty"`
	RecordedAtUTC   time.Time        `json:"recorded_at_utc"`
}

// Key is the material identity the candidate was produced for.
func (c CalibrationCandidate) Key() Key {
	return Key{SampleID: c.SampleID, MaterialDigest: c.MaterialDigest, Dimension: c.Dimension}
}

// Validate checks the closed shape of a candidate record.
func (c CalibrationCandidate) Validate() error {
	if c.SchemaVersion != CalibrationCandidateSchemaVersion {
		return Reject(ReasonInvalidMaterial, "unsupported calibration candidate schema version %q", c.SchemaVersion)
	}
	if c.Purpose != CalibrationCandidatePurpose {
		return Reject(ReasonInvalidMaterial, "calibration candidates require purpose=%s", CalibrationCandidatePurpose)
	}
	if strings.TrimSpace(c.ManifestDigest) == "" || strings.TrimSpace(c.ScorerDigest) == "" {
		return Reject(ReasonInvalidMaterial, "candidate requires manifest and scorer identities")
	}
	if c.ScorerKind != ScorerFake && c.ScorerKind != ScorerReal {
		return Reject(ReasonInvalidMaterial, "candidate requires a fake or real scorer kind")
	}
	if strings.TrimSpace(c.SampleID) == "" || strings.TrimSpace(c.MaterialDigest) == "" {
		return Reject(ReasonInvalidMaterial, "candidate requires sample and material identity")
	}
	if c.InvocationIndex < 0 {
		return Reject(ReasonInvalidMaterial, "candidate invocation index must not be negative")
	}
	switch c.Outcome {
	case CandidatePrediction:
		if c.Prediction == nil || !ValidLabel(*c.Prediction) {
			return Reject(ReasonInvalidOutput, "a prediction outcome requires a frozen label")
		}
		if c.ReasonCode != "" {
			return Reject(ReasonInvalidOutput, "a validated prediction must not carry a failure reason")
		}
	case CandidateError, CandidateSkip:
		if c.Prediction != nil {
			return Reject(ReasonInvalidOutput, "an error or skip must not carry a prediction")
		}
		if !ValidReasonCode(c.ReasonCode) {
			return Reject(ReasonInvalidOutput, "an error or skip requires a closed reason code")
		}
	default:
		return Reject(ReasonInvalidOutput, "candidate requires a known outcome")
	}
	return nil
}

// LoadCalibrationCandidates reads a strict JSONL candidate file.
func LoadCalibrationCandidates(path string) ([]CalibrationCandidate, error) {
	records, err := DecodeJSONL[CalibrationCandidate](path)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return nil, err
		}
	}
	return records, nil
}

// LoadRegisteredManifest reads a strict JSON manifest.
func LoadRegisteredManifest(path string) (RegisteredManifest, error) {
	var manifest RegisteredManifest
	if err := DecodeJSONStrict(path, &manifest); err != nil {
		return RegisteredManifest{}, err
	}
	return manifest, nil
}

// Validate checks the manifest's closed values and the presence of every
// registered parameter.
func (m RegisteredManifest) Validate() error {
	if m.SchemaVersion != RegisteredManifestSchemaVersion {
		return Reject(ReasonInvalidMaterial, "unsupported manifest schema version %q", m.SchemaVersion)
	}
	if strings.TrimSpace(m.ManifestID) == "" {
		return Reject(ReasonInvalidMaterial, "manifest requires manifest_id")
	}
	if m.RegisteredAtUTC.IsZero() {
		return Reject(ReasonInvalidMaterial, "manifest requires a registration time")
	}
	if err := validateDimension(m.Dimension); err != nil {
		return err
	}
	if err := m.Scorer.Validate(); err != nil {
		return err
	}
	if m.Budget != nil {
		if err := m.Budget.Validate(); err != nil {
			return err
		}
		if m.Budget.Digest != "" && m.Budget.Digest != m.Scorer.BudgetDigest {
			return Reject(ReasonInvalidMaterial, "scorer budget digest does not match the registered budget")
		}
	}
	if err := m.Splits.Validate(); err != nil {
		return err
	}
	if err := m.Thresholds.Validate(); err != nil {
		return err
	}
	if err := m.Bootstrap.Validate(); err != nil {
		return err
	}
	seen := map[BiasControlName]bool{}
	for _, control := range m.BiasControls {
		if !validBiasControl(control.Name) {
			return Reject(ReasonInvalidMaterial, "unknown bias control")
		}
		if seen[control.Name] {
			return Reject(ReasonInvalidMaterial, "duplicate bias control")
		}
		seen[control.Name] = true
	}
	for _, material := range m.ApprovedMaterials {
		if strings.TrimSpace(material.SampleID) == "" || strings.TrimSpace(material.MaterialDigest) == "" ||
			strings.TrimSpace(material.MaterialGroupID) == "" {
			return Reject(ReasonInvalidMaterial, "approved material requires sample, group and digest")
		}
	}
	return nil
}

// ManifestDigest is the canonical identity of the registered manifest. Editing
// any registered value changes it, which invalidates candidates recorded under
// the previous registration.
func (m RegisteredManifest) Digest() string {
	return canonicalDigest(m)
}

// Validate checks that the split plan is the frozen rule with disjoint,
// non-empty group lists.
func (p SplitPlan) Validate() error {
	if p.SplitRule != SplitRuleByMaterialGroup {
		return Reject(ReasonInvalidMaterial, "splits must use the frozen rule %s", SplitRuleByMaterialGroup)
	}
	if p.RepresentativeRule != RepresentativeRuleFirstEligibleBySampleID {
		return Reject(ReasonInvalidMaterial, "splits must use the frozen representative rule %s", RepresentativeRuleFirstEligibleBySampleID)
	}
	if len(p.DebugGroups) == 0 || len(p.HoldoutGroups) == 0 {
		return Reject(ReasonInvalidMaterial, "splits require debug and holdout groups")
	}
	seen := map[string]string{}
	for _, pair := range []struct {
		split  string
		groups []string
	}{{SplitDebug, p.DebugGroups}, {SplitHoldout, p.HoldoutGroups}} {
		for _, group := range pair.groups {
			if strings.TrimSpace(group) == "" {
				return Reject(ReasonInvalidMaterial, "split groups must not be empty")
			}
			if previous, ok := seen[group]; ok {
				// A same-source variant in both splits would leak between them.
				return Reject(ReasonInvalidMaterial, "material group %s appears in both %s and %s splits", group, previous, pair.split)
			}
			seen[group] = pair.split
		}
	}
	return nil
}

// SplitOf returns the registered split for a material group.
func (p SplitPlan) SplitOf(group string) (string, bool) {
	for _, candidate := range p.DebugGroups {
		if candidate == group {
			return SplitDebug, true
		}
	}
	for _, candidate := range p.HoldoutGroups {
		if candidate == group {
			return SplitHoldout, true
		}
	}
	return "", false
}

func validBiasControl(name BiasControlName) bool {
	for _, candidate := range RequiredBiasControls() {
		if candidate == name {
			return true
		}
	}
	return false
}

// Validate requires every registered acceptance parameter to be present, finite
// and in range. There is no default pass line.
func (t Thresholds) Validate() error {
	if t.MinIndependentTotal <= 0 {
		return Reject(ReasonInvalidMaterial, "thresholds require a positive minimum independent total")
	}
	if len(t.MinPerClass) == 0 || len(t.MinRecall) == 0 {
		return Reject(ReasonInvalidMaterial, "thresholds require per-class minimums and recalls")
	}
	for label, minimum := range t.MinPerClass {
		if !ValidLabel(label) || minimum < 0 {
			return Reject(ReasonInvalidMaterial, "per-class minimums require frozen labels and non-negative counts")
		}
	}
	for label, minimum := range t.MinRecall {
		if !ValidLabel(label) {
			return Reject(ReasonInvalidMaterial, "recall minimums require frozen labels")
		}
		if math.IsNaN(minimum) || math.IsInf(minimum, 0) || minimum < 0 || minimum > 1 {
			return Reject(ReasonInvalidMaterial, "recall minimums must be finite probabilities")
		}
	}
	for name, value := range map[string]*float64{
		"min_agreement":             t.MinAgreement,
		"min_agreement_lower_bound": t.MinAgreementLowerBound,
		"min_kappa":                 t.MinKappa,
		"min_kappa_lower_bound":     t.MinKappaLowerBound,
		"max_missing_rate":          t.MaxMissingRate,
		"bias_control_tolerance":    t.BiasControlTolerance,
	} {
		if value == nil {
			return Reject(ReasonInvalidMaterial, "thresholds require %s", name)
		}
		if math.IsNaN(*value) || math.IsInf(*value, 0) {
			return Reject(ReasonInvalidMaterial, "%s must be finite", name)
		}
	}
	if *t.MaxMissingRate < 0 || *t.MaxMissingRate > 1 {
		return Reject(ReasonInvalidMaterial, "max_missing_rate must be a probability")
	}
	for _, value := range []*float64{t.MinAgreement, t.MinAgreementLowerBound, t.MinKappa, t.MinKappaLowerBound} {
		if *value < -1 || *value > 1 {
			return Reject(ReasonInvalidMaterial, "agreement and kappa thresholds must be within [-1,1]")
		}
	}
	if *t.BiasControlTolerance < 0 {
		return Reject(ReasonInvalidMaterial, "bias_control_tolerance must not be negative")
	}
	return nil
}

// Validate requires a finite, reproducible resampling policy.
func (r BootstrapRule) Validate() error {
	if r.Resamples <= 0 {
		return Reject(ReasonInvalidMaterial, "bootstrap requires a positive resample count")
	}
	if r.Level <= 0 || r.Level >= 1 {
		return Reject(ReasonInvalidMaterial, "bootstrap requires a level within (0,1)")
	}
	if math.IsNaN(r.MaxInvalidFraction) || r.MaxInvalidFraction < 0 || r.MaxInvalidFraction > 1 {
		return Reject(ReasonInvalidMaterial, "max_invalid_fraction must be a probability")
	}
	return nil
}

// SplitDigest is the identity of the registered split assignment.
func SplitDigest(plan SplitPlan) string {
	debug := append([]string(nil), plan.DebugGroups...)
	holdout := append([]string(nil), plan.HoldoutGroups...)
	sort.Strings(debug)
	sort.Strings(holdout)
	return canonicalDigest(struct {
		Normalization      string   `json:"normalization"`
		SplitRule          string   `json:"split_rule"`
		RepresentativeRule string   `json:"representative_rule"`
		DebugGroups        []string `json:"debug_groups"`
		HoldoutGroups      []string `json:"holdout_groups"`
	}{ContentNormalizationVersion, plan.SplitRule, plan.RepresentativeRule, debug, holdout})
}

// RulesDigest is the identity of the registered acceptance policy.
func RulesDigest(manifest RegisteredManifest) string {
	controls := append([]BiasControl(nil), manifest.BiasControls...)
	sort.Slice(controls, func(i, j int) bool { return controls[i].Name < controls[j].Name })
	return canonicalDigest(struct {
		Normalization string        `json:"normalization"`
		Dimension     Dimension     `json:"dimension"`
		Thresholds    Thresholds    `json:"thresholds"`
		Bootstrap     BootstrapRule `json:"bootstrap"`
		BiasControls  []BiasControl `json:"bias_controls"`
	}{ContentNormalizationVersion, manifest.Dimension, manifest.Thresholds, manifest.Bootstrap, controls})
}

// AcceptanceReason is a closed explanation for a calibration verdict.
type AcceptanceReason string

// Closed acceptance reasons.
const (
	AcceptanceReasonNotCalibrated          AcceptanceReason = "not_calibrated"
	AcceptanceReasonInsufficientGroups     AcceptanceReason = "insufficient_independent_groups"
	AcceptanceReasonInsufficientPerClass   AcceptanceReason = "insufficient_per_class_labels"
	AcceptanceReasonAgreementBelow         AcceptanceReason = "agreement_below_threshold"
	AcceptanceReasonAgreementIntervalBelow AcceptanceReason = "agreement_interval_below_threshold"
	AcceptanceReasonKappaBelow             AcceptanceReason = "kappa_below_threshold"
	AcceptanceReasonKappaIntervalBelow     AcceptanceReason = "kappa_interval_below_threshold"
	AcceptanceReasonRecallBelow            AcceptanceReason = "recall_below_threshold"
	AcceptanceReasonMissingRateAbove       AcceptanceReason = "missing_rate_above_threshold"
	AcceptanceReasonBiasControlUnverified  AcceptanceReason = "bias_control_unverified"
	AcceptanceReasonIntervalNotEstimable   AcceptanceReason = "interval_not_estimable"
	AcceptanceReasonSingleClassLabels      AcceptanceReason = "single_class_human_labels"
	AcceptanceReasonConstantPrediction     AcceptanceReason = "constant_prediction"
	AcceptanceReasonNoPairs                AcceptanceReason = "no_paired_predictions"
)

// ConditionResult records one registered condition and what was observed.
type ConditionResult struct {
	Name      string `json:"name"`
	Required  string `json:"required"`
	Observed  string `json:"observed"`
	Satisfied bool   `json:"satisfied"`
}

// Acceptance is the calibration verdict with its reasons and conditions.
type Acceptance struct {
	Status     AcceptanceStatus   `json:"status"`
	Reasons    []AcceptanceReason `json:"reasons"`
	Conditions []ConditionResult  `json:"conditions"`
	DecidedOn  string             `json:"decided_on"`
	AppliesTo  string             `json:"applies_to"`
}

// HumanReliabilityReason is a closed explanation for the reliability status.
type HumanReliabilityReason string

// Closed human reliability reasons.
const (
	HumanReliabilityTwoIndependent HumanReliabilityReason = "two_independent_blind_annotators"
	HumanReliabilitySinglePerson   HumanReliabilityReason = "single_annotator"
	HumanReliabilityNoShared       HumanReliabilityReason = "no_shared_materials"
)

// HumanReliability is human-vs-human agreement on pre-adjudication blind labels.
// It is reported separately from human-vs-Judge agreement and never substituted
// by it.
type HumanReliability struct {
	Status     EstimateStatus         `json:"status"`
	Reason     HumanReliabilityReason `json:"reason"`
	Paired     int                    `json:"paired"`
	Missing    int                    `json:"missing"`
	Agreement  Estimate               `json:"agreement"`
	Kappa      Estimate               `json:"kappa"`
	Annotators []string               `json:"annotators"`
	Note       string                 `json:"note"`
}

// SampleFlow is the sample flow accounting. Denominators are explicit so a
// scorer cannot be judged only on the invocations that succeeded.
type SampleFlow struct {
	Planned             int            `json:"planned"`
	UniqueMaterials     int            `json:"unique_materials"`
	IndependentGroups   int            `json:"independent_groups"`
	InitialLabels       int            `json:"initial_labels"`
	Disagreements       int            `json:"disagreements"`
	Resolved            int            `json:"resolved"`
	DuplicatesCollapsed int            `json:"duplicates_collapsed"`
	Reviews             int            `json:"reviews"`
	Applicable          int            `json:"applicable"`
	NotApplicable       int            `json:"not_applicable"`
	Paired              int            `json:"paired"`
	Errors              int            `json:"errors"`
	Skips               int            `json:"skips"`
	MissingRate         *float64       `json:"missing_rate"`
	MissingRateStatus   EstimateStatus `json:"missing_rate_status"`
}

// InvocationRecord is one planned invocation's recorded outcome.
type InvocationRecord struct {
	SampleID        string           `json:"sample_id"`
	MaterialGroupID string           `json:"material_group_id"`
	MaterialDigest  string           `json:"material_digest"`
	InvocationIndex int              `json:"invocation_index"`
	Outcome         CandidateOutcome `json:"outcome"`
	Prediction      *Label           `json:"prediction"`
	ReasonCode      ReasonCode       `json:"reason_code,omitempty"`
}

// InvocationSummary counts the outcomes of one invocation index.
type InvocationSummary struct {
	InvocationIndex int `json:"invocation_index"`
	Planned         int `json:"planned"`
	Predictions     int `json:"predictions"`
	Errors          int `json:"errors"`
	Skips           int `json:"skips"`
}

// scoredPair is one human label paired with the Judge's validated prediction for
// the same material.
type scoredPair struct {
	SampleID string
	Human    Label
	Judge    Label
}

// SplitReport is the per-set statistic block. FullSet holds the full-material
// point estimates; Primary holds the pre-registered representative view that the
// acceptance decision uses, so duplicated source variants cannot narrow an
// interval.
type SplitReport struct {
	Name                    string      `json:"name"`
	Materials               int         `json:"materials"`
	Groups                  int         `json:"groups"`
	FullSet                 PairMetrics `json:"full_set"`
	Primary                 PairMetrics `json:"primary"`
	RepresentativeSampleIDs []string    `json:"representative_sample_ids"`
}

// CalibrationReport is the local evidence for one scorer/dimension/run.
type CalibrationReport struct {
	ReportSchemaVersion string              `json:"report_schema_version"`
	GeneratedAtUTC      time.Time           `json:"generated_at_utc"`
	Identity            CalibrationIdentity `json:"identity"`
	Scorer              ScorerIdentity      `json:"scorer"`
	Manifest            RegisteredManifest  `json:"registered_manifest"`
	ManifestDigest      string              `json:"manifest_digest"`
	PrimaryInvocation   int                 `json:"primary_invocation_index"`
	SampleFlow          SampleFlow          `json:"sample_flow"`
	Invocations         []InvocationSummary `json:"invocations"`
	InvocationRecords   []InvocationRecord  `json:"invocation_records"`
	Splits              []SplitReport       `json:"splits"`
	HumanReliability    HumanReliability    `json:"human_reliability"`
	Acceptance          Acceptance          `json:"acceptance"`
	Unverified          []string            `json:"unverified,omitempty"`
}

// CalibrationInput bundles the offline replay inputs.
type CalibrationInput struct {
	CalibrationID    string
	Materials        []Material
	Annotations      []Annotation
	Reviews          []Review
	Candidates       []CalibrationCandidate
	Manifest         RegisteredManifest
	Dataset          string
	EvaluatorVersion string
	Profile          string
	CodeVersion      string
	SourceDigest     string
	Now              time.Time
}

// BuildCalibration replays the registered statistics and decides acceptance.
func BuildCalibration(input CalibrationInput) (CalibrationReport, error) {
	manifest := input.Manifest
	if err := manifest.Validate(); err != nil {
		return CalibrationReport{}, err
	}
	index, err := IndexMaterials(input.Materials)
	if err != nil {
		return CalibrationReport{}, err
	}
	truths, err := ResolveAnnotations(index, input.Annotations, input.Reviews)
	if err != nil {
		return CalibrationReport{}, err
	}

	// Candidate provenance is checked before pairing: a candidate produced under
	// a different manifest, scorer or kind does not describe this measurement.
	manifestDigest := manifest.Digest()
	approved := map[Key]bool{}
	for _, material := range manifest.ApprovedMaterials {
		approved[Key{SampleID: material.SampleID, MaterialDigest: material.MaterialDigest, Dimension: manifest.Dimension}] = true
	}
	invocationRecords := make([]InvocationRecord, 0, len(input.Candidates))
	invocationCounts := map[int]*InvocationSummary{}
	for _, candidate := range input.Candidates {
		if err := candidate.Validate(); err != nil {
			return CalibrationReport{}, err
		}
		if candidate.ManifestDigest != manifestDigest {
			return CalibrationReport{}, Reject(ReasonCalibrationMismatch,
				"candidate for sample=%s was recorded under a different manifest", candidate.SampleID)
		}
		if candidate.ScorerDigest != manifest.Scorer.Digest || candidate.ScorerKind != manifest.Scorer.Kind {
			return CalibrationReport{}, Reject(ReasonCalibrationMismatch,
				"candidate for sample=%s was recorded by a different scorer", candidate.SampleID)
		}
		if candidate.Dimension != manifest.Dimension {
			return CalibrationReport{}, Reject(ReasonCalibrationMismatch,
				"candidate for sample=%s was recorded for a different dimension", candidate.SampleID)
		}
		material, ok := index[candidate.Key()]
		if !ok {
			return CalibrationReport{}, Reject(ReasonInvalidMaterial,
				"candidate references sample=%s which is not part of the material set", candidate.SampleID)
		}
		if candidate.MaterialGroupID != material.MaterialGroupID {
			return CalibrationReport{}, Reject(ReasonCalibrationMismatch,
				"candidate for sample=%s declares a different material group", candidate.SampleID)
		}
		if len(approved) > 0 && !approved[candidate.Key()] {
			return CalibrationReport{}, Reject(ReasonCalibrationMismatch,
				"sample=%s was not approved by the registered manifest", candidate.SampleID)
		}
		summary, ok := invocationCounts[candidate.InvocationIndex]
		if !ok {
			summary = &InvocationSummary{InvocationIndex: candidate.InvocationIndex}
			invocationCounts[candidate.InvocationIndex] = summary
		}
		summary.Planned++
		switch candidate.Outcome {
		case CandidatePrediction:
			summary.Predictions++
		case CandidateError:
			summary.Errors++
		case CandidateSkip:
			summary.Skips++
		}
		invocationRecords = append(invocationRecords, InvocationRecord{
			SampleID: candidate.SampleID, MaterialGroupID: candidate.MaterialGroupID,
			MaterialDigest: candidate.MaterialDigest, InvocationIndex: candidate.InvocationIndex,
			Outcome: candidate.Outcome, Prediction: candidate.Prediction, ReasonCode: candidate.ReasonCode,
		})
	}
	sort.Slice(invocationRecords, func(i, j int) bool {
		left, right := invocationRecords[i], invocationRecords[j]
		if left.SampleID != right.SampleID {
			return left.SampleID < right.SampleID
		}
		return left.InvocationIndex < right.InvocationIndex
	})
	invocations := make([]InvocationSummary, 0, len(invocationCounts))
	for _, summary := range invocationCounts {
		invocations = append(invocations, *summary)
	}
	sort.Slice(invocations, func(i, j int) bool { return invocations[i].InvocationIndex < invocations[j].InvocationIndex })

	// Pair each resolved truth with the primary invocation's validated
	// prediction. Extra invocation indices are reported separately and never
	// pooled: repeating a round does not create independent evidence.
	debugGroups := map[string][]scoredPair{}
	holdoutGroups := map[string][]scoredPair{}
	allGroups := map[string][]scoredPair{}
	applicableGroups := map[string][]scoredPair{}
	primaryByKey := map[Key]CalibrationCandidate{}
	seenPrimary := map[Key]bool{}
	for _, candidate := range input.Candidates {
		if candidate.InvocationIndex != PrimaryInvocationIndex {
			continue
		}
		if seenPrimary[candidate.Key()] {
			return CalibrationReport{}, Reject(ReasonInvalidMaterial,
				"sample=%s has more than one recorded invocation at the primary index", candidate.SampleID)
		}
		seenPrimary[candidate.Key()] = true
		primaryByKey[candidate.Key()] = candidate
	}

	flow := SampleFlow{
		UniqueMaterials: len(index), InitialLabels: len(input.Annotations),
		Disagreements: len(truths.Pending), DuplicatesCollapsed: truths.Duplicates, Reviews: truths.Reviews,
	}
	groupSet := map[string]bool{}
	for _, material := range input.Materials {
		if material.Applicability == ApplicabilityApplicable {
			groupSet[material.MaterialGroupID] = true
		}
	}
	flow.IndependentGroups = len(groupSet)

	for _, truth := range truths.Truths {
		material, ok := index[truth.Key]
		if !ok {
			continue
		}
		applicable := material.Applicability == ApplicabilityApplicable
		candidate, hasCandidate := primaryByKey[truth.Key]
		if !applicable {
			flow.NotApplicable++
		} else {
			flow.Applicable++
			if truth.Origin == TruthOriginResolvedReview {
				flow.Resolved++
			}
			switch {
			case !hasCandidate:
				// A planned invocation with no record at all is still a gap in
				// the denominator, not an absence of evidence.
				flow.Errors++
			case candidate.Outcome == CandidateSkip:
				flow.Skips++
			case candidate.Outcome != CandidatePrediction:
				flow.Errors++
			}
		}
		if !hasCandidate || candidate.Outcome != CandidatePrediction {
			continue
		}
		split, registered := manifest.Splits.SplitOf(material.MaterialGroupID)
		if !registered {
			return CalibrationReport{}, Reject(ReasonInvalidMaterial,
				"material group %s is not assigned to a registered split", material.MaterialGroupID)
		}
		pair := scoredPair{SampleID: material.SampleID, Human: truth.Label, Judge: *candidate.Prediction}
		allGroups[material.MaterialGroupID] = append(allGroups[material.MaterialGroupID], pair)
		if !applicable {
			continue
		}
		if split == SplitDebug {
			debugGroups[material.MaterialGroupID] = append(debugGroups[material.MaterialGroupID], pair)
		} else {
			holdoutGroups[material.MaterialGroupID] = append(holdoutGroups[material.MaterialGroupID], pair)
		}
		applicableGroups[material.MaterialGroupID] = append(applicableGroups[material.MaterialGroupID], pair)
		flow.Paired++
	}
	// One planned invocation per applicable material: the adapter is serial and
	// never retries, so "planned" is not a count of attempts.
	flow.Planned = flow.Applicable
	if flow.Planned > 0 {
		rate := float64(flow.Planned-flow.Paired) / float64(flow.Planned)
		flow.MissingRate = &rate
		flow.MissingRateStatus = EstimateEstimated
	} else {
		flow.MissingRateStatus = EstimateNotEstimable
	}

	reports := make([]SplitReport, 0, 4)
	for _, item := range []struct {
		name   string
		groups map[string][]scoredPair
	}{
		{SplitDebug, debugGroups},
		{SplitHoldout, holdoutGroups},
		{"all_labels", allGroups},
		{"applicable", applicableGroups},
	} {
		report, err := buildSplitReport(item.name, item.groups, manifest.Bootstrap)
		if err != nil {
			return CalibrationReport{}, err
		}
		reports = append(reports, report)
	}

	reliability := computeHumanReliability(truths)

	identity := CalibrationIdentity{
		SchemaVersion: "1", CalibrationID: input.CalibrationID, Dimension: manifest.Dimension,
		Dataset: input.Dataset, EvaluatorVersion: input.EvaluatorVersion, Profile: input.Profile,
		ScorerKind: manifest.Scorer.Kind, ScorerDigest: manifest.Scorer.Digest,
		MaterialSetDigest:   MaterialSetDigest(input.Materials),
		AnnotationSetDigest: AnnotationSetDigest(input.Annotations, input.Reviews),
		SplitDigest:         SplitDigest(manifest.Splits), RulesDigest: RulesDigest(manifest),
		EnvironmentDigest: EnvironmentDigest(LocalEnvironment(input.CodeVersion, input.Profile, input.SourceDigest)),
		SourceDigest:      input.SourceDigest,
		CreatedAtUTC:      input.Now.UTC(),
	}
	identity.Digest = CalibrationIdentityDigest(identity)

	acceptance := decideAcceptance(manifest, identity, findSplit(reports, SplitHoldout), flow)

	return CalibrationReport{
		ReportSchemaVersion: CalibrationReportSchemaVersion,
		GeneratedAtUTC:      input.Now.UTC(),
		Identity:            identity, Scorer: manifest.Scorer, Manifest: manifest,
		ManifestDigest: manifestDigest, PrimaryInvocation: PrimaryInvocationIndex,
		SampleFlow: flow, Invocations: invocations, InvocationRecords: invocationRecords,
		Splits: reports, HumanReliability: reliability, Acceptance: acceptance,
		Unverified: []string{
			"real Judge quality, real human double review and provider revision stability are unverified unless separately executed",
		},
	}, nil
}

// findSplit returns one computed split block by name.
func findSplit(reports []SplitReport, name string) SplitReport {
	for _, report := range reports {
		if report.Name == name {
			return report
		}
	}
	return SplitReport{Name: name}
}

// buildSplitReport computes the full-material point estimates and the
// representative view for one set of paired predictions.
//
// The representative rule is the registered one and is applied before any label
// or prediction is inspected: within each independent material group, sort by
// sample_id and keep the first pair. Statistics on the representative view are
// what the acceptance decision reads, so repeating a source variant cannot
// narrow an interval.
func buildSplitReport(name string, groups map[string][]scoredPair, rule BootstrapRule) (SplitReport, error) {
	groupIDs := make([]string, 0, len(groups))
	for groupID := range groups {
		groupIDs = append(groupIDs, groupID)
	}
	sort.Strings(groupIDs)

	full := make([]GroupPairs, 0, len(groupIDs))
	primary := make([]GroupPairs, 0, len(groupIDs))
	representatives := make([]string, 0, len(groupIDs))
	materials := 0
	for _, groupID := range groupIDs {
		pairs := append([]scoredPair(nil), groups[groupID]...)
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].SampleID < pairs[j].SampleID })
		converted := make([]LabelPair, 0, len(pairs))
		for _, pair := range pairs {
			converted = append(converted, LabelPair{Human: pair.Human, Judge: pair.Judge})
		}
		full = append(full, GroupPairs{GroupID: groupID, Pairs: converted})
		primary = append(primary, GroupPairs{GroupID: groupID, Pairs: converted[:1]})
		representatives = append(representatives, pairs[0].SampleID)
		materials += len(converted)
	}
	fullSet, err := ComputePairMetrics(full, rule)
	if err != nil {
		return SplitReport{}, err
	}
	primarySet, err := ComputePairMetrics(primary, rule)
	if err != nil {
		return SplitReport{}, err
	}
	return SplitReport{
		Name: name, Materials: materials, Groups: len(groupIDs),
		FullSet: fullSet, Primary: primarySet, RepresentativeSampleIDs: representatives,
	}, nil
}

func computeHumanReliability(truths AnnotationSet) HumanReliability {
	annotators := map[string]bool{}
	pairs := make([]LabelPair, 0)
	missing := 0
	for _, truth := range truths.Truths {
		blind := map[string]Label{}
		for _, label := range truth.Labels {
			if !label.Blind {
				continue
			}
			annotators[label.AnnotatorID] = true
			if _, ok := blind[label.AnnotatorID]; !ok {
				blind[label.AnnotatorID] = label.Label
			}
		}
		if len(blind) < 2 {
			missing++
			continue
		}
		ids := make([]string, 0, len(blind))
		for id := range blind {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				pairs = append(pairs, LabelPair{Human: blind[ids[i]], Judge: blind[ids[j]]})
			}
		}
	}
	names := make([]string, 0, len(annotators))
	for name := range annotators {
		names = append(names, name)
	}
	sort.Strings(names)

	reliability := HumanReliability{
		Status: EstimateNotEstimable, Reason: HumanReliabilitySinglePerson,
		Paired: len(pairs), Missing: missing, Annotators: names,
		Note: "inter-annotator reliability requires two independent blind annotators on the same material; a single annotator or a self-review does not establish it",
	}
	if len(annotators) < 2 {
		reliability.Agreement = notEstimable(NotEstimableNoPairs, 0)
		reliability.Kappa = notEstimable(NotEstimableNoPairs, 0)
		return reliability
	}
	if len(pairs) == 0 {
		reliability.Reason = HumanReliabilityNoShared
		reliability.Agreement = notEstimable(NotEstimableNoPairs, 0)
		reliability.Kappa = notEstimable(NotEstimableNoPairs, 0)
		return reliability
	}
	matrix := NewConfusionMatrix()
	for _, pair := range pairs {
		if err := matrix.Add(pair.Human, pair.Judge); err != nil {
			reliability.Agreement = notEstimable(NotEstimableNoPairs, 0)
			reliability.Kappa = notEstimable(NotEstimableNoPairs, 0)
			return reliability
		}
	}
	reliability.Status = EstimateEstimated
	reliability.Reason = HumanReliabilityTwoIndependent
	reliability.Agreement = matrix.Agreement()
	reliability.Kappa = matrix.Kappa()
	return reliability
}

// decideAcceptance applies the registered thresholds to the holdout primary
// statistics only. Debug evidence never authorizes a scorer.
func decideAcceptance(manifest RegisteredManifest, identity CalibrationIdentity, holdout SplitReport, flow SampleFlow) Acceptance {
	acceptance := Acceptance{
		Status: AcceptanceUnusable, Reasons: make([]AcceptanceReason, 0),
		DecidedOn: SplitHoldout + "_primary_applicable",
		// The verdict only applies to the registered identity and scope; it is
		// not a general statement about the provider or the model family.
		AppliesTo: fmt.Sprintf("dimension=%s dataset=%s profile=%s scorer_kind=%s rules=%s split=%s",
			identity.Dimension, identity.Dataset, identity.Profile, identity.ScorerKind,
			short(identity.RulesDigest), short(identity.SplitDigest)),
	}
	if holdout.Primary.Pairs == 0 {
		acceptance.Status = AcceptanceNotCalibrated
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonNotCalibrated, AcceptanceReasonNoPairs)
		return acceptance
	}
	if name, degenerate := holdout.Primary.IsDegenerate(); degenerate {
		if name == "single_class_human_labels" {
			acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonSingleClassLabels)
		} else {
			acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonConstantPrediction)
		}
	}

	thresholds := manifest.Thresholds
	add := func(name, required, observed string, satisfied bool) {
		acceptance.Conditions = append(acceptance.Conditions, ConditionResult{
			Name: name, Required: required, Observed: observed, Satisfied: satisfied,
		})
	}

	groupsSatisfied := holdout.Primary.Groups >= thresholds.MinIndependentTotal
	add("independent_groups", fmt.Sprintf(">=%d", thresholds.MinIndependentTotal),
		fmt.Sprintf("%d", holdout.Primary.Groups), groupsSatisfied)
	if !groupsSatisfied {
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonInsufficientGroups)
	}

	perClassSatisfied := true
	labels := make([]Label, 0, len(thresholds.MinPerClass))
	for label := range thresholds.MinPerClass {
		labels = append(labels, label)
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i] < labels[j] })
	for _, label := range labels {
		minimum := thresholds.MinPerClass[label]
		observed := holdout.Primary.HumanCounts[label]
		satisfied := observed >= minimum
		perClassSatisfied = perClassSatisfied && satisfied
		add("min_per_class:"+string(label), fmt.Sprintf(">=%d", minimum), fmt.Sprintf("%d", observed), satisfied)
	}
	if !perClassSatisfied {
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonInsufficientPerClass)
	}

	agreementSatisfied := estimateAtLeast(holdout.Primary.Agreement, *thresholds.MinAgreement)
	add("agreement", fmt.Sprintf(">=%v", *thresholds.MinAgreement), formatEstimate(holdout.Primary.Agreement), agreementSatisfied)
	if !agreementSatisfied {
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonAgreementBelow)
	}

	intervalSatisfied := holdout.Primary.AgreementCI != nil && holdout.Primary.AgreementCI.Low >= *thresholds.MinAgreementLowerBound
	observedInterval := "not_estimable"
	if holdout.Primary.AgreementCI != nil {
		observedInterval = fmt.Sprintf("low=%v", holdout.Primary.AgreementCI.Low)
	}
	add("agreement_interval_lower_bound", fmt.Sprintf(">=%v", *thresholds.MinAgreementLowerBound), observedInterval, intervalSatisfied)
	if !intervalSatisfied {
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonAgreementIntervalBelow)
	}

	kappaSatisfied := estimateAtLeast(holdout.Primary.Kappa, *thresholds.MinKappa)
	add("kappa", fmt.Sprintf(">=%v", *thresholds.MinKappa), formatEstimate(holdout.Primary.Kappa), kappaSatisfied)
	if !kappaSatisfied {
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonKappaBelow)
	}

	kappaIntervalSatisfied := holdout.Primary.KappaCI.Interval != nil && holdout.Primary.KappaCI.Interval.Low >= *thresholds.MinKappaLowerBound
	observedKappaInterval := "not_estimable"
	if holdout.Primary.KappaCI.Interval != nil {
		observedKappaInterval = fmt.Sprintf("low=%v", holdout.Primary.KappaCI.Interval.Low)
	}
	add("kappa_interval_lower_bound", fmt.Sprintf(">=%v", *thresholds.MinKappaLowerBound), observedKappaInterval, kappaIntervalSatisfied)
	if !kappaIntervalSatisfied {
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonKappaIntervalBelow)
	}
	if holdout.Primary.AgreementCI == nil || holdout.Primary.KappaCI.Interval == nil {
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonIntervalNotEstimable)
	}

	recallSatisfied := true
	recallLabels := make([]Label, 0, len(thresholds.MinRecall))
	for label := range thresholds.MinRecall {
		recallLabels = append(recallLabels, label)
	}
	sort.Slice(recallLabels, func(i, j int) bool { return recallLabels[i] < recallLabels[j] })
	for _, label := range recallLabels {
		minimum := thresholds.MinRecall[label]
		estimate := holdout.Primary.Recall[label]
		satisfied := estimateAtLeast(estimate, minimum)
		recallSatisfied = recallSatisfied && satisfied
		add("recall:"+string(label), fmt.Sprintf(">=%v", minimum), formatEstimate(estimate), satisfied)
	}
	if !recallSatisfied {
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonRecallBelow)
	}

	missingSatisfied := flow.MissingRate != nil && *flow.MissingRate <= *thresholds.MaxMissingRate
	observedMissing := "not_estimable"
	if flow.MissingRate != nil {
		observedMissing = fmt.Sprintf("%v", *flow.MissingRate)
	}
	add("missing_rate", fmt.Sprintf("<=%v", *thresholds.MaxMissingRate), observedMissing, missingSatisfied)
	if !missingSatisfied {
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonMissingRateAbove)
	}

	present := map[BiasControlName]BiasControl{}
	for _, control := range manifest.BiasControls {
		present[control.Name] = control
	}
	controlsSatisfied := true
	for _, name := range RequiredBiasControls() {
		control, ok := present[name]
		verified := ok && control.Verified
		controlsSatisfied = controlsSatisfied && verified
		add("bias_control:"+string(name), "verified", fmt.Sprintf("%t", verified), verified)
	}
	if !controlsSatisfied {
		acceptance.Reasons = append(acceptance.Reasons, AcceptanceReasonBiasControlUnverified)
	}

	if len(acceptance.Reasons) == 0 {
		acceptance.Status = AcceptanceUsable
	}
	return acceptance
}

func estimateAtLeast(estimate Estimate, minimum float64) bool {
	return estimate.Status == EstimateEstimated && estimate.Value != nil && *estimate.Value >= minimum
}

func formatEstimate(estimate Estimate) string {
	if estimate.Status != EstimateEstimated || estimate.Value == nil {
		return "not_estimable"
	}
	return fmt.Sprintf("%v", *estimate.Value)
}
