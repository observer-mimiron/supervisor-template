package semantic

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// A Material is one evaluation sample: the exact task intent and the exact
// public answer that a human label and a Judge prediction both refer to.
//
// The hard verdict is not enough to identify what was evaluated. A Case can pass
// with a relevant answer or with an off-topic one, so a label attached only to
// "case X passed" cannot be calibrated. Material identity therefore binds the
// dataset/case identity, the existing hard verdict digest, the frozen dimension,
// the rubric/reference/projection identities and the answer bytes.

// MaterialSchemaVersion is the record envelope version.
const MaterialSchemaVersion = "1"

// IntentSource is the closed set of allowed task-intent provenance. A business
// goal is not automatically an evaluation intent: it becomes one only when it is
// registered as a separately reviewed source.
type IntentSource string

// SourceApproval is the closed set of allowed material provenance. Only
// synthetic or explicitly human-approved anonymous material may be stored.
type SourceApproval string

// Closed intent sources.
const (
	IntentSourceReviewedBusinessGoal IntentSource = "reviewed_case_business_goal"
	IntentSourceApprovedAnonymous    IntentSource = "approved_anonymous_summary"
)

// Closed source approvals.
const (
	SourceApprovalSynthetic         SourceApproval = "synthetic"
	SourceApprovalApprovedAnonymous SourceApproval = "approved_anonymous"
)

// Reference is an optional short external fact a dimension genuinely needs.
// Adding or editing one changes the material identity and therefore invalidates
// any calibration that was computed without it.
type Reference struct {
	Version string `json:"version"`
	Text    string `json:"text"`
	Digest  string `json:"digest"`
}

// MaterialSpec is the declared part of a material. The public answer is not
// declared here: it either comes from the evaluated run or is written down as
// material, never synthesized from a hard verdict.
type MaterialSpec struct {
	SchemaVersion       string              `json:"schema_version"`
	SampleID            string              `json:"sample_id"`
	MaterialGroupID     string              `json:"material_group_id"`
	DatasetName         string              `json:"dataset_name"`
	DatasetVersion      string              `json:"dataset_version"`
	CaseID              string              `json:"case_id"`
	CaseVersion         string              `json:"case_version"`
	VerdictDigest       string              `json:"verdict_digest"`
	Dimension           Dimension           `json:"dimension"`
	IntentSource        IntentSource        `json:"intent_source"`
	TaskIntent          string              `json:"task_intent"`
	SourceApproval      SourceApproval      `json:"source_approval"`
	Applicability       Applicability       `json:"applicability"`
	NotApplicableReason NotApplicableReason `json:"not_applicable_reason,omitempty"`
	Reference           *Reference          `json:"reference,omitempty"`
}

// Material is a MaterialSpec plus the evaluated answer and the computed
// identities. MaterialDigest is the canonical identity of the whole record.
type Material struct {
	MaterialSpec
	PublicAnswer   string          `json:"public_answer"`
	Rubric         ContentIdentity `json:"rubric"`
	Projection     ContentIdentity `json:"projection"`
	MaterialDigest string          `json:"material_digest"`
}

// PublicEvent is the minimal public event projection a material needs. The
// semantic package does not depend on the runtime runner: it consumes only the
// already-public event type and data map.
type PublicEvent struct {
	Type string
	Data map[string]string
}

// AnswerFromEvents returns the last public `text` event's exact content.
//
// The last text event is the final public answer; earlier ones are progress.
// A trailing event with empty content means there is no usable answer, and this
// function reports that instead of falling back to an earlier event or
// inventing text.
func AnswerFromEvents(events []PublicEvent) (string, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != "text" {
			continue
		}
		content := events[index].Data["content"]
		if strings.TrimSpace(content) == "" {
			return "", false
		}
		return content, true
	}
	return "", false
}

// BuildMaterial binds a declared material spec to the answer produced by the
// evaluated run. It never synthesizes an answer: a spec that requires an answer
// while the run produced none is rejected as material_missing.
func BuildMaterial(spec MaterialSpec, events []PublicEvent) (Material, error) {
	answer, _ := AnswerFromEvents(events)
	if spec.Applicability == ApplicabilityNotApplicable {
		// The sample contract says no public answer is required; the answer
		// field must stay empty so an available answer cannot be excused from
		// evaluation by declaring it not applicable.
		answer = ""
	}
	material := Material{
		MaterialSpec: spec,
		PublicAnswer: answer,
		Rubric:       FrozenRubricIdentity(),
		Projection:   FrozenProjectionIdentity(),
	}
	material.MaterialDigest = MaterialDigest(material)
	if err := material.Validate(); err != nil {
		return Material{}, err
	}
	return material, nil
}

// MaterialDigest is the canonical identity of a material. It covers the exact
// UTF-8 intent and answer bytes, the frozen rubric/reference/projection
// identities and the hard identity it was taken from. Timestamps and run IDs are
// deliberately absent so an independent re-run of the same material keeps the
// same identity — but the evaluated text is never excluded, because that is the
// thing being judged.
func MaterialDigest(material Material) string {
	type referencePart struct {
		Version string `json:"version"`
		Text    string `json:"text"`
		Digest  string `json:"digest"`
	}
	type canonical struct {
		Normalization   string          `json:"normalization"`
		SchemaVersion   string          `json:"schema_version"`
		SampleID        string          `json:"sample_id"`
		MaterialGroupID string          `json:"material_group_id"`
		DatasetName     string          `json:"dataset_name"`
		DatasetVersion  string          `json:"dataset_version"`
		CaseID          string          `json:"case_id"`
		CaseVersion     string          `json:"case_version"`
		VerdictDigest   string          `json:"verdict_digest"`
		Dimension       Dimension       `json:"dimension"`
		IntentSource    IntentSource    `json:"intent_source"`
		TaskIntent      string          `json:"task_intent"`
		PublicAnswer    string          `json:"public_answer"`
		SourceApproval  SourceApproval  `json:"source_approval"`
		Applicability   Applicability   `json:"applicability"`
		NotApplicable   string          `json:"not_applicable_reason"`
		Rubric          ContentIdentity `json:"rubric"`
		Projection      ContentIdentity `json:"projection"`
		Reference       *referencePart  `json:"reference"`
	}
	value := canonical{
		Normalization: ContentNormalizationVersion, SchemaVersion: material.SchemaVersion,
		SampleID: material.SampleID, MaterialGroupID: material.MaterialGroupID,
		DatasetName: material.DatasetName, DatasetVersion: material.DatasetVersion,
		CaseID: material.CaseID, CaseVersion: material.CaseVersion,
		VerdictDigest: material.VerdictDigest, Dimension: material.Dimension,
		IntentSource: material.IntentSource, TaskIntent: material.TaskIntent,
		PublicAnswer: material.PublicAnswer, SourceApproval: material.SourceApproval,
		Applicability: material.Applicability, NotApplicable: string(material.NotApplicableReason),
		Rubric: material.Rubric, Projection: material.Projection,
	}
	if material.Reference != nil {
		value.Reference = &referencePart{
			Version: material.Reference.Version, Text: material.Reference.Text, Digest: material.Reference.Digest,
		}
	}
	return canonicalDigest(value)
}

// Key is the identity a human label and a Judge prediction both bind to.
type Key struct {
	SampleID       string    `json:"sample_id"`
	MaterialDigest string    `json:"material_digest"`
	Dimension      Dimension `json:"dimension"`
}

// Key returns the material's binding key.
func (m Material) Key() Key {
	return Key{SampleID: m.SampleID, MaterialDigest: m.MaterialDigest, Dimension: m.Dimension}
}

// String renders a stable key for maps and messages.
func (k Key) String() string {
	return k.SampleID + "|" + k.MaterialDigest + "|" + string(k.Dimension)
}

// Validate checks the material against the frozen contract. Missing required
// answers are material_missing, never not_applicable.
func (m Material) Validate() error {
	if m.SchemaVersion != MaterialSchemaVersion {
		return Reject(ReasonInvalidMaterial, "unsupported material schema version %q", m.SchemaVersion)
	}
	for field, value := range map[string]string{
		"sample_id": m.SampleID, "material_group_id": m.MaterialGroupID,
		"dataset_name": m.DatasetName, "dataset_version": m.DatasetVersion,
		"case_id": m.CaseID, "case_version": m.CaseVersion,
		"verdict_digest": m.VerdictDigest, "task_intent": m.TaskIntent,
	} {
		if strings.TrimSpace(value) == "" {
			return Reject(ReasonInvalidMaterial, "material requires %s", field)
		}
	}
	if err := validateDimension(m.Dimension); err != nil {
		return err
	}
	if m.IntentSource != IntentSourceReviewedBusinessGoal && m.IntentSource != IntentSourceApprovedAnonymous {
		return Reject(ReasonInvalidMaterial, "material requires a known intent source")
	}
	if m.SourceApproval != SourceApprovalSynthetic && m.SourceApproval != SourceApprovalApprovedAnonymous {
		return Reject(ReasonInvalidMaterial, "material requires an approved source")
	}
	if !ValidApplicability(m.Applicability) {
		return Reject(ReasonInvalidMaterial, "material requires a known applicability")
	}
	switch m.Applicability {
	case ApplicabilityApplicable:
		if strings.TrimSpace(m.PublicAnswer) == "" {
			return Reject(ReasonMaterialMissing, "applicable material %s has no public answer", m.SampleID)
		}
		if m.NotApplicableReason != "" {
			return Reject(ReasonInvalidMaterial, "applicable material must not declare a not-applicable reason")
		}
	case ApplicabilityNotApplicable:
		if !ValidNotApplicableReason(m.NotApplicableReason) {
			return Reject(ReasonInvalidMaterial, "not-applicable material requires the frozen not-applicable reason")
		}
		if m.PublicAnswer != "" {
			// Declaring an available answer "not applicable" would excuse it
			// from evaluation; the sample contract cannot do that.
			return Reject(ReasonInvalidMaterial, "not-applicable material must not carry a public answer")
		}
	}
	if m.Rubric != FrozenRubricIdentity() {
		return Reject(ReasonInvalidMaterial, "material rubric identity does not match the frozen rubric")
	}
	if m.Projection != FrozenProjectionIdentity() {
		return Reject(ReasonInvalidMaterial, "material projection identity does not match the frozen projection")
	}
	if m.Reference != nil {
		if strings.TrimSpace(m.Reference.Version) == "" || strings.TrimSpace(m.Reference.Text) == "" {
			return Reject(ReasonInvalidMaterial, "reference requires a version and bounded text")
		}
		if m.Reference.Digest != DigestText(m.Reference.Text) {
			return Reject(ReasonInvalidMaterial, "reference digest does not match its text")
		}
	}
	if err := checkBounded("task_intent", m.TaskIntent); err != nil {
		return err
	}
	if err := checkBounded("public_answer", m.PublicAnswer); err != nil {
		return err
	}
	if err := checkBounded("rubric", RubricAnswerRelevanceV1); err != nil {
		return err
	}
	if m.Reference != nil {
		if err := checkBounded("reference", m.Reference.Text); err != nil {
			return err
		}
	}
	if m.MaterialDigest == "" {
		return Reject(ReasonInvalidMaterial, "material requires material_digest")
	}
	if m.MaterialDigest != MaterialDigest(m) {
		return Reject(ReasonInvalidMaterial, "material_digest does not match the material content")
	}
	return nil
}

// checkBounded rejects invalid UTF-8 and over-limit whitelist text. Over-limit
// material is refused rather than truncated: truncating would silently change
// the task being judged.
func checkBounded(field, value string) error {
	if !utf8.ValidString(value) {
		return Reject(ReasonInvalidMaterial, "%s is not valid UTF-8", field)
	}
	if len(value) > MaxProjectionFieldBytes {
		return Reject(ReasonInputTooLarge, "%s exceeds %d bytes", field, MaxProjectionFieldBytes)
	}
	return nil
}

// LoadMaterials reads and validates a strict JSONL material file. Duplicate
// (sample_id, material_digest) pairs are refused: the same material twice would
// silently double its weight in every statistic.
func LoadMaterials(path string) ([]Material, error) {
	records, err := DecodeJSONL[Material](path)
	if err != nil {
		return nil, err
	}
	seen := make(map[Key]bool, len(records))
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return nil, err
		}
		key := record.Key()
		if seen[key] {
			return nil, Reject(ReasonInvalidMaterial, "duplicate material %s in %s", record.SampleID, path)
		}
		seen[key] = true
	}
	return records, nil
}

// MaterialIndex indexes loaded materials by their binding key.
type MaterialIndex map[Key]Material

// IndexMaterials builds a lookup for annotation and prediction resolution.
func IndexMaterials(materials []Material) (MaterialIndex, error) {
	index := make(MaterialIndex, len(materials))
	for _, material := range materials {
		key := material.Key()
		if existing, ok := index[key]; ok {
			return nil, Reject(ReasonInvalidMaterial, "duplicate material %s", existing.SampleID)
		}
		index[key] = material
	}
	return index, nil
}

// Lookup resolves a key to a loaded material, rejecting unknown references and
// identity mismatches instead of matching loosely by sample ID.
func (i MaterialIndex) Lookup(key Key) (Material, error) {
	material, ok := i[key]
	if !ok {
		return Material{}, Reject(ReasonInvalidMaterial,
			"unknown material reference sample=%s digest=%s", key.SampleID, short(key.MaterialDigest))
	}
	return material, nil
}

func short(digest string) string {
	if len(digest) <= 12 {
		return digest
	}
	return digest[:12]
}

func (k Key) GoString() string { return fmt.Sprintf("%s@%s", k.SampleID, short(k.MaterialDigest)) }
