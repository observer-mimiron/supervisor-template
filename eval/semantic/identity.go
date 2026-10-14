package semantic

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Identity is what makes a calibration reusable or not. "The same Judge" is not
// a model name: changing the prompt, the rubric, the reference, the input
// projection, the parameters or the material set changes the function that was
// calibrated. Each of those gets its own digest so a reuse failure can name the
// item that changed instead of just refusing.

// ScorerKind is the closed fake/real provenance of a scorer. Fake evidence can
// validate contracts but can never authorize a real Judge.
type ScorerKind string

// Closed scorer kinds.
const (
	ScorerFake ScorerKind = "fake"
	ScorerReal ScorerKind = "real"
)

// ProviderRevisionStatus records whether a provider revision could be pinned.
// A model alias that cannot be pinned is recorded as unverified instead of
// pretending provider drift has been detected.
type ProviderRevisionStatus string

// Closed provider revision statuses.
const (
	ProviderRevisionPinned     ProviderRevisionStatus = "pinned"
	ProviderRevisionUnverified ProviderRevisionStatus = "unverified"
)

// ScorerIdentity names exactly the scoring function that was calibrated.
type ScorerIdentity struct {
	SchemaVersion          string                 `json:"schema_version"`
	Kind                   ScorerKind             `json:"kind"`
	Provider               string                 `json:"provider"`
	Model                  string                 `json:"model"`
	ProviderRevision       string                 `json:"provider_revision,omitempty"`
	ProviderRevisionStatus ProviderRevisionStatus `json:"provider_revision_status"`
	Parameters             map[string]string      `json:"parameters,omitempty"`
	PromptVersion          string                 `json:"prompt_version"`
	PromptDigest           string                 `json:"prompt_digest"`
	Rubric                 ContentIdentity        `json:"rubric"`
	Projection             ContentIdentity        `json:"projection"`
	MarkerSet              ContentIdentity        `json:"marker_set"`
	ReferenceSetDigest     string                 `json:"reference_set_digest"`
	BudgetDigest           string                 `json:"budget_digest"`
	Digest                 string                 `json:"digest"`
}

// BudgetIdentity is a finite, digestible execution budget. A missing or
// unbounded budget must never lead to an outbound request.
type BudgetIdentity struct {
	MaxCalls       int    `json:"max_calls"`
	TimeoutMS      int    `json:"timeout_ms"`
	MaxInputBytes  int    `json:"max_input_bytes"`
	MaxOutputBytes int    `json:"max_output_bytes"`
	Digest         string `json:"digest"`
}

// EnvironmentSummary is an allow-listed, non-secret description of the local
// environment. It deliberately has no free-form map: an environment identity
// must not become a place where secrets or arbitrary context leak in.
type EnvironmentSummary struct {
	GoVersion    string `json:"go_version"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	CodeVersion  string `json:"code_version"`
	Profile      string `json:"profile"`
	SourceDigest string `json:"source_digest"`
}

// CalibrationIdentity is the full list a reused calibration must still match.
type CalibrationIdentity struct {
	SchemaVersion       string     `json:"schema_version"`
	CalibrationID       string     `json:"calibration_id"`
	Dimension           Dimension  `json:"dimension"`
	Dataset             string     `json:"dataset"`
	EvaluatorVersion    string     `json:"evaluator_version"`
	Profile             string     `json:"profile"`
	ScorerKind          ScorerKind `json:"scorer_kind"`
	ScorerDigest        string     `json:"scorer_digest"`
	MaterialSetDigest   string     `json:"material_set_digest"`
	AnnotationSetDigest string     `json:"annotation_set_digest"`
	SplitDigest         string     `json:"split_digest"`
	RulesDigest         string     `json:"rules_digest"`
	EnvironmentDigest   string     `json:"environment_digest"`
	SourceDigest        string     `json:"source_digest"`
	CreatedAtUTC        time.Time  `json:"created_at_utc"`
	Digest              string     `json:"digest"`
}

// secretParameterKeys are the parameter names that must never appear in an
// identity. Storing or digesting a credential would make the identity itself a
// secret-bearing artifact.
var secretParameterKeys = []string{
	"key", "token", "secret", "password", "passwd", "credential", "authorization", "auth", "dsn", "cookie",
}

// Validate checks the scorer identity for closed values and for
// secret-bearing parameters.
func (s ScorerIdentity) Validate() error {
	if s.SchemaVersion != "1" {
		return Reject(ReasonInvalidMaterial, "unsupported scorer schema version %q", s.SchemaVersion)
	}
	if s.Kind != ScorerFake && s.Kind != ScorerReal {
		return Reject(ReasonInvalidMaterial, "scorer requires a fake or real kind")
	}
	if strings.TrimSpace(s.Provider) == "" || strings.TrimSpace(s.Model) == "" {
		return Reject(ReasonInvalidMaterial, "scorer requires provider and model")
	}
	if s.ProviderRevisionStatus != ProviderRevisionPinned && s.ProviderRevisionStatus != ProviderRevisionUnverified {
		return Reject(ReasonInvalidMaterial, "scorer requires a provider revision status")
	}
	if s.ProviderRevisionStatus == ProviderRevisionPinned && strings.TrimSpace(s.ProviderRevision) == "" {
		return Reject(ReasonInvalidMaterial, "a pinned provider revision requires its value")
	}
	if strings.TrimSpace(s.PromptVersion) == "" || strings.TrimSpace(s.PromptDigest) == "" {
		return Reject(ReasonInvalidMaterial, "scorer requires a prompt version and digest")
	}
	if s.Rubric != FrozenRubricIdentity() {
		return Reject(ReasonInvalidMaterial, "scorer rubric identity does not match the frozen rubric")
	}
	if s.Projection != FrozenProjectionIdentity() {
		return Reject(ReasonInvalidMaterial, "scorer projection identity does not match the frozen projection")
	}
	if s.MarkerSet != FrozenSensitiveMarkerSetIdentity() {
		return Reject(ReasonInvalidMaterial, "scorer marker set identity does not match the frozen marker set")
	}
	if err := checkParametersAreNotSecret(s.Parameters); err != nil {
		return err
	}
	if s.Digest != "" && s.Digest != ScorerDigest(s) {
		return Reject(ReasonInvalidMaterial, "scorer digest does not match the scorer content")
	}
	return nil
}

func checkParametersAreNotSecret(parameters map[string]string) error {
	for key, value := range parameters {
		lowered := strings.ToLower(strings.TrimSpace(key))
		for _, forbidden := range secretParameterKeys {
			if strings.Contains(lowered, forbidden) {
				return Reject(ReasonSensitiveInput, "scorer parameter %q must not carry credentials", key)
			}
		}
		if marker, found := ContainsSensitiveMarker(value); found {
			return Reject(ReasonSensitiveInput, "scorer parameter %q contains the known marker %q", key, marker)
		}
	}
	return nil
}

// ScorerDigest is the canonical digest of the scorer identity, excluding the
// digest field itself.
func ScorerDigest(scorer ScorerIdentity) string {
	type canonical struct {
		SchemaVersion          string                 `json:"schema_version"`
		Kind                   ScorerKind             `json:"kind"`
		Provider               string                 `json:"provider"`
		Model                  string                 `json:"model"`
		ProviderRevision       string                 `json:"provider_revision,omitempty"`
		ProviderRevisionStatus ProviderRevisionStatus `json:"provider_revision_status"`
		Parameters             map[string]string      `json:"parameters,omitempty"`
		PromptVersion          string                 `json:"prompt_version"`
		PromptDigest           string                 `json:"prompt_digest"`
		Rubric                 ContentIdentity        `json:"rubric"`
		Projection             ContentIdentity        `json:"projection"`
		MarkerSet              ContentIdentity        `json:"marker_set"`
		ReferenceSetDigest     string                 `json:"reference_set_digest"`
		BudgetDigest           string                 `json:"budget_digest"`
	}
	return canonicalDigest(canonical{
		SchemaVersion: scorer.SchemaVersion, Kind: scorer.Kind, Provider: scorer.Provider,
		Model: scorer.Model, ProviderRevision: scorer.ProviderRevision,
		ProviderRevisionStatus: scorer.ProviderRevisionStatus, Parameters: scorer.Parameters,
		PromptVersion: scorer.PromptVersion, PromptDigest: scorer.PromptDigest,
		Rubric: scorer.Rubric, Projection: scorer.Projection, MarkerSet: scorer.MarkerSet,
		ReferenceSetDigest: scorer.ReferenceSetDigest, BudgetDigest: scorer.BudgetDigest,
	})
}

// Validate checks that every budget dimension is finite and positive.
func (b BudgetIdentity) Validate() error {
	if b.MaxCalls <= 0 || b.TimeoutMS <= 0 || b.MaxInputBytes <= 0 || b.MaxOutputBytes <= 0 {
		return Reject(ReasonBudgetMissing, "budget requires positive call, time and size limits")
	}
	if b.Digest != "" && b.Digest != BudgetDigest(b) {
		return Reject(ReasonInvalidMaterial, "budget digest does not match the budget content")
	}
	return nil
}

// BudgetDigest is the canonical digest of a finite budget.
func BudgetDigest(budget BudgetIdentity) string {
	return canonicalDigest(struct {
		MaxCalls       int `json:"max_calls"`
		TimeoutMS      int `json:"timeout_ms"`
		MaxInputBytes  int `json:"max_input_bytes"`
		MaxOutputBytes int `json:"max_output_bytes"`
	}{budget.MaxCalls, budget.TimeoutMS, budget.MaxInputBytes, budget.MaxOutputBytes})
}

// MaterialSetDigest is the identity of the exact material set, independent of
// file order: the same samples in a different order are the same corpus.
func MaterialSetDigest(materials []Material) string {
	keys := make([]string, 0, len(materials))
	for _, material := range materials {
		keys = append(keys, material.Key().String())
	}
	sort.Strings(keys)
	return canonicalDigest(struct {
		Normalization string   `json:"normalization"`
		Keys          []string `json:"keys"`
	}{ContentNormalizationVersion, keys})
}

// ReferenceSetDigest is the identity of the reference texts used by a material
// set. References are optional, so an absent set has its own stable digest.
func ReferenceSetDigest(materials []Material) string {
	values := make([]string, 0, len(materials))
	for _, material := range materials {
		if material.Reference == nil {
			continue
		}
		values = append(values, material.Reference.Version+":"+material.Reference.Digest)
	}
	sort.Strings(values)
	return canonicalDigest(struct {
		Normalization string   `json:"normalization"`
		References    []string `json:"references"`
	}{ContentNormalizationVersion, values})
}

// AnnotationSetDigest is the identity of the human truth, including reviews and
// original labels. A changed label is a different annotation set.
func AnnotationSetDigest(annotations []Annotation, reviews []Review) string {
	annotationKeys := make([]string, 0, len(annotations))
	for _, annotation := range annotations {
		annotationKeys = append(annotationKeys, strings.Join([]string{
			annotation.Key().String(), string(annotation.Label), annotation.AnnotatorID,
			annotation.AnnotatedAtUTC.UTC().Format(time.RFC3339Nano), fmt.Sprintf("%t", annotation.Blind),
		}, "|"))
	}
	sort.Strings(annotationKeys)
	reviewKeys := make([]string, 0, len(reviews))
	for _, review := range reviews {
		sources := append([]string(nil), review.SourceAnnotationIDs...)
		sort.Strings(sources)
		reviewKeys = append(reviewKeys, strings.Join([]string{
			review.Key().String(), string(review.AdoptedLabel), review.ReviewerID, fmt.Sprintf("%t", review.Resolved),
			strings.Join(sources, ","),
		}, "|"))
	}
	sort.Strings(reviewKeys)
	return canonicalDigest(struct {
		Normalization string   `json:"normalization"`
		Annotations   []string `json:"annotations"`
		Reviews       []string `json:"reviews"`
	}{ContentNormalizationVersion, annotationKeys, reviewKeys})
}

// EnvironmentDigest is the identity of the allow-listed environment summary.
func EnvironmentDigest(summary EnvironmentSummary) string {
	return canonicalDigest(struct {
		Normalization string             `json:"normalization"`
		Summary       EnvironmentSummary `json:"summary"`
	}{ContentNormalizationVersion, summary})
}

// CalibrationIdentityDigest is the canonical digest of an identity list,
// excluding the digest field itself. Times are excluded: the identity is what
// was measured, not when it was recorded.
func CalibrationIdentityDigest(identity CalibrationIdentity) string {
	type canonical struct {
		SchemaVersion       string     `json:"schema_version"`
		CalibrationID       string     `json:"calibration_id"`
		Dimension           Dimension  `json:"dimension"`
		Dataset             string     `json:"dataset"`
		EvaluatorVersion    string     `json:"evaluator_version"`
		Profile             string     `json:"profile"`
		ScorerKind          ScorerKind `json:"scorer_kind"`
		ScorerDigest        string     `json:"scorer_digest"`
		MaterialSetDigest   string     `json:"material_set_digest"`
		AnnotationSetDigest string     `json:"annotation_set_digest"`
		SplitDigest         string     `json:"split_digest"`
		RulesDigest         string     `json:"rules_digest"`
		EnvironmentDigest   string     `json:"environment_digest"`
		SourceDigest        string     `json:"source_digest"`
	}
	return canonicalDigest(canonical{
		SchemaVersion: identity.SchemaVersion, CalibrationID: identity.CalibrationID,
		Dimension: identity.Dimension, Dataset: identity.Dataset,
		EvaluatorVersion: identity.EvaluatorVersion, Profile: identity.Profile,
		ScorerKind: identity.ScorerKind, ScorerDigest: identity.ScorerDigest,
		MaterialSetDigest: identity.MaterialSetDigest, AnnotationSetDigest: identity.AnnotationSetDigest,
		SplitDigest: identity.SplitDigest, RulesDigest: identity.RulesDigest,
		EnvironmentDigest: identity.EnvironmentDigest, SourceDigest: identity.SourceDigest,
	})
}

// InvalidatedFields lists the identity items that differ between a calibration
// and the run trying to reuse it. An empty result means the calibration still
// describes the same measurement; anything else names the item that changed
// instead of refusing with a single opaque "mismatch".
func InvalidatedFields(calibration, current CalibrationIdentity) []string {
	checks := []struct {
		name     string
		previous string
		now      string
	}{
		{"dimension", string(calibration.Dimension), string(current.Dimension)},
		{"dataset", calibration.Dataset, current.Dataset},
		{"evaluator_version", calibration.EvaluatorVersion, current.EvaluatorVersion},
		{"profile", calibration.Profile, current.Profile},
		{"scorer_kind", string(calibration.ScorerKind), string(current.ScorerKind)},
		{"scorer_digest", calibration.ScorerDigest, current.ScorerDigest},
		{"material_set_digest", calibration.MaterialSetDigest, current.MaterialSetDigest},
		{"annotation_set_digest", calibration.AnnotationSetDigest, current.AnnotationSetDigest},
		{"split_digest", calibration.SplitDigest, current.SplitDigest},
		{"rules_digest", calibration.RulesDigest, current.RulesDigest},
		{"environment_digest", calibration.EnvironmentDigest, current.EnvironmentDigest},
		{"source_digest", calibration.SourceDigest, current.SourceDigest},
	}
	invalidated := make([]string, 0)
	for _, check := range checks {
		if check.previous != check.now {
			invalidated = append(invalidated, check.name)
		}
	}
	return invalidated
}

// LocalEnvironment builds the allow-listed environment summary for this process.
func LocalEnvironment(codeVersion, profile, sourceDigest string) EnvironmentSummary {
	return EnvironmentSummary{
		GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH,
		CodeVersion: codeVersion, Profile: profile, SourceDigest: sourceDigest,
	}
}

// SourceDigest hashes an explicit, allow-listed list of source files. Paths are
// sorted and each file's own digest is included, so the summary is stable and
// bound to the exact revision that produced a calibration.
func SourceDigest(paths []string) (string, error) {
	ordered := append([]string(nil), paths...)
	sort.Strings(ordered)
	lines := make([]string, 0, len(ordered))
	for _, path := range ordered {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		lines = append(lines, path+"\x00"+DigestBytes(data))
	}
	return canonicalDigest(struct {
		Normalization string   `json:"normalization"`
		Files         []string `json:"files"`
	}{ContentNormalizationVersion, lines}), nil
}

// EffectiveConfigDigest summarizes an allow-listed, non-secret configuration
// subset. Keys and values are screened: a secret must be neither stored nor
// digested to stand in for a configuration identity.
func EffectiveConfigDigest(values map[string]string) (string, error) {
	if err := checkParametersAreNotSecret(values); err != nil {
		return "", err
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, key+"="+values[key])
	}
	return canonicalDigest(struct {
		Normalization string   `json:"normalization"`
		Entries       []string `json:"entries"`
	}{ContentNormalizationVersion, lines}), nil
}
