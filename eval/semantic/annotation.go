package semantic

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// A human label is an independent record of one person's judgement about one
// material. It is deliberately not a `human_disposition`: a disposition says what
// to do about a failure, a semantic label says whether the answer addressed the
// task. Reusing the disposition field would overload it with a meaning it never
// had and would let a quality score masquerade as a contract decision.
//
// Original annotations are immutable. Review does not rewrite a label; it adds a
// separate record that names the labels it adjudicated, so a disagreement stays
// visible after it is resolved.

// AnnotationSchemaVersion is the annotation record envelope version.
const AnnotationSchemaVersion = "1"

// ReviewSchemaVersion is the review record envelope version.
const ReviewSchemaVersion = "1"

// MaxAnnotationNoteBytes bounds an optional local note.
const MaxAnnotationNoteBytes = 1024

// Annotation is one human label on one exact material.
type Annotation struct {
	SchemaVersion  string    `json:"schema_version"`
	AnnotationID   string    `json:"annotation_id"`
	SampleID       string    `json:"sample_id"`
	MaterialDigest string    `json:"material_digest"`
	Dimension      Dimension `json:"dimension"`
	Label          Label     `json:"label"`
	AnnotatorID    string    `json:"annotator_id"`
	AnnotatedAtUTC time.Time `json:"annotated_at_utc"`
	Blind          bool      `json:"blind"`
	Note           string    `json:"note,omitempty"`
}

// Review is a human adjudication of the annotations for one material. It never
// replaces them.
type Review struct {
	SchemaVersion       string    `json:"schema_version"`
	ReviewID            string    `json:"review_id"`
	SampleID            string    `json:"sample_id"`
	MaterialDigest      string    `json:"material_digest"`
	Dimension           Dimension `json:"dimension"`
	ReviewerID          string    `json:"reviewer_id"`
	ReviewedAtUTC       time.Time `json:"reviewed_at_utc"`
	AdoptedLabel        Label     `json:"adopted_label"`
	SourceAnnotationIDs []string  `json:"source_annotation_ids"`
	Resolved            bool      `json:"resolved"`
	Note                string    `json:"note,omitempty"`
}

// TruthOrigin says how a calibration truth was established, so a single
// unreviewed label is never presented as inter-annotator agreement.
type TruthOrigin string

// Closed truth origins.
const (
	TruthOriginSingleBlindLabel TruthOrigin = "single_blind_label"
	TruthOriginUnanimousBlind   TruthOrigin = "unanimous_blind_labels"
	TruthOriginResolvedReview   TruthOrigin = "resolved_human_review"
)

// AnnotatorLabel is one original, immutable label kept for audit and for
// human-vs-human reliability, which must be computed before adjudication.
type AnnotatorLabel struct {
	AnnotationID   string    `json:"annotation_id"`
	AnnotatorID    string    `json:"annotator_id"`
	Label          Label     `json:"label"`
	Blind          bool      `json:"blind"`
	AnnotatedAtUTC time.Time `json:"annotated_at_utc"`
}

// Truth is the single calibration label for one material, with its provenance.
type Truth struct {
	Key                   Key              `json:"key"`
	Label                 Label            `json:"label"`
	Origin                TruthOrigin      `json:"origin"`
	AnnotatorIDs          []string         `json:"annotator_ids"`
	ReviewID              string           `json:"review_id,omitempty"`
	IndependentlyReviewed bool             `json:"independently_reviewed"`
	Labels                []AnnotatorLabel `json:"labels"`
}

// PendingSample is a material whose annotators disagree and which has no
// resolved review. It produces no truth and is excluded from calibration.
type PendingSample struct {
	Key          Key      `json:"key"`
	AnnotatorIDs []string `json:"annotator_ids"`
	Labels       []Label  `json:"labels"`
}

// AnnotationSet is the resolved view of the local annotation files.
type AnnotationSet struct {
	Truths     []Truth         `json:"truths"`
	Pending    []PendingSample `json:"pending"`
	Duplicates int             `json:"duplicates"`
	Reviews    int             `json:"reviews"`
}

// LoadAnnotations reads a strict JSONL annotation file.
func LoadAnnotations(path string) ([]Annotation, error) {
	records, err := DecodeJSONL[Annotation](path)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return nil, err
		}
		if seen[record.AnnotationID] {
			return nil, Reject(ReasonInvalidAnnotation, "duplicate annotation id %s", record.AnnotationID)
		}
		seen[record.AnnotationID] = true
	}
	return records, nil
}

// LoadReviews reads a strict JSONL review file.
func LoadReviews(path string) ([]Review, error) {
	records, err := DecodeJSONL[Review](path)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(records))
	for _, record := range records {
		if err := record.Validate(); err != nil {
			return nil, err
		}
		if seen[record.ReviewID] {
			return nil, Reject(ReasonInvalidAnnotation, "duplicate review id %s", record.ReviewID)
		}
		seen[record.ReviewID] = true
	}
	return records, nil
}

// Validate checks one annotation against the frozen contract.
func (a Annotation) Validate() error {
	if a.SchemaVersion != AnnotationSchemaVersion {
		return Reject(ReasonInvalidAnnotation, "unsupported annotation schema version %q", a.SchemaVersion)
	}
	if strings.TrimSpace(a.AnnotationID) == "" {
		return Reject(ReasonInvalidAnnotation, "annotation requires annotation_id")
	}
	if strings.TrimSpace(a.SampleID) == "" || strings.TrimSpace(a.MaterialDigest) == "" {
		return Reject(ReasonInvalidAnnotation, "annotation requires sample_id and material_digest")
	}
	if err := validateDimension(a.Dimension); err != nil {
		return Reject(ReasonInvalidAnnotation, "annotation requires the frozen dimension")
	}
	if !ValidLabel(a.Label) {
		return Reject(ReasonInvalidAnnotation, "annotation %s has an unknown label", a.AnnotationID)
	}
	if strings.TrimSpace(a.AnnotatorID) == "" {
		return Reject(ReasonInvalidAnnotation, "annotation requires a pseudonymous annotator_id")
	}
	if a.AnnotatedAtUTC.IsZero() {
		return Reject(ReasonInvalidAnnotation, "annotation %s requires a UTC timestamp", a.AnnotationID)
	}
	return checkNote(a.Note)
}

// Validate checks one review against the frozen contract.
func (r Review) Validate() error {
	if r.SchemaVersion != ReviewSchemaVersion {
		return Reject(ReasonInvalidAnnotation, "unsupported review schema version %q", r.SchemaVersion)
	}
	if strings.TrimSpace(r.ReviewID) == "" {
		return Reject(ReasonInvalidAnnotation, "review requires review_id")
	}
	if strings.TrimSpace(r.SampleID) == "" || strings.TrimSpace(r.MaterialDigest) == "" {
		return Reject(ReasonInvalidAnnotation, "review requires sample_id and material_digest")
	}
	if err := validateDimension(r.Dimension); err != nil {
		return Reject(ReasonInvalidAnnotation, "review requires the frozen dimension")
	}
	if strings.TrimSpace(r.ReviewerID) == "" {
		return Reject(ReasonInvalidAnnotation, "review requires reviewer_id")
	}
	if r.ReviewedAtUTC.IsZero() {
		return Reject(ReasonInvalidAnnotation, "review %s requires a UTC timestamp", r.ReviewID)
	}
	if !ValidLabel(r.AdoptedLabel) {
		return Reject(ReasonInvalidAnnotation, "review %s has an unknown adopted label", r.ReviewID)
	}
	if len(r.SourceAnnotationIDs) == 0 {
		return Reject(ReasonInvalidAnnotation, "review %s must reference the annotations it adjudicated", r.ReviewID)
	}
	return checkNote(r.Note)
}

// Key is the material identity the annotation binds to.
func (a Annotation) Key() Key {
	return Key{SampleID: a.SampleID, MaterialDigest: a.MaterialDigest, Dimension: a.Dimension}
}

// Key is the material identity the review binds to.
func (r Review) Key() Key {
	return Key{SampleID: r.SampleID, MaterialDigest: r.MaterialDigest, Dimension: r.Dimension}
}

// checkNote bounds and screens an optional local note. Notes stay local, but a
// credential written into a note is still a credential written to disk.
func checkNote(note string) error {
	if note == "" {
		return nil
	}
	if len(note) > MaxAnnotationNoteBytes {
		return Reject(ReasonInputTooLarge, "annotation note exceeds %d bytes", MaxAnnotationNoteBytes)
	}
	if marker, found := ContainsSensitiveMarker(note); found {
		return Reject(ReasonSensitiveInput, "annotation note contains the known marker %q", marker)
	}
	return nil
}

// annotatorState is one person's single label for one material, plus every
// original record retained for audit.
type annotatorState struct {
	label  Label
	labels []AnnotatorLabel
}

// ResolveAnnotations validates annotations and reviews against the loaded
// materials and produces at most one truth per material.
//
// Rules that keep this honest:
//   - an unknown or identity-mismatched reference is rejected, never matched by
//     sample ID alone;
//   - one annotator contributes one label: identical repeats are collapsed, and
//     a conflicting repeat is rejected rather than picked;
//   - cross-annotator disagreement is retained and produces no truth until a
//     resolved review adjudicates it;
//   - a single unreviewed label is truth with TruthOriginSingleBlindLabel, and a
//     self-review never claims independent review.
func ResolveAnnotations(materials MaterialIndex, annotations []Annotation, reviews []Review) (AnnotationSet, error) {
	set := AnnotationSet{Truths: make([]Truth, 0), Pending: make([]PendingSample, 0)}
	grouped := make(map[Key]map[string]*annotatorState)
	for _, annotation := range annotations {
		if err := annotation.Validate(); err != nil {
			return AnnotationSet{}, err
		}
		if _, err := materials.Lookup(annotation.Key()); err != nil {
			return AnnotationSet{}, Reject(ReasonInvalidAnnotation,
				"annotation %s references an unknown material sample=%s", annotation.AnnotationID, annotation.SampleID)
		}
		annotators, ok := grouped[annotation.Key()]
		if !ok {
			annotators = make(map[string]*annotatorState)
			grouped[annotation.Key()] = annotators
		}
		state, ok := annotators[annotation.AnnotatorID]
		if !ok {
			annotators[annotation.AnnotatorID] = &annotatorState{
				label: annotation.Label,
				labels: []AnnotatorLabel{{
					AnnotationID: annotation.AnnotationID, AnnotatorID: annotation.AnnotatorID,
					Label: annotation.Label, Blind: annotation.Blind, AnnotatedAtUTC: annotation.AnnotatedAtUTC,
				}},
			}
			continue
		}
		// One person, one material, one label. A repeat is not an extra sample.
		set.Duplicates++
		if state.label != annotation.Label {
			return AnnotationSet{}, Reject(ReasonInvalidAnnotation,
				"annotator %s gave conflicting labels for sample=%s", annotation.AnnotatorID, annotation.SampleID)
		}
	}

	reviewsByKey := make(map[Key][]Review)
	for _, review := range reviews {
		if err := review.Validate(); err != nil {
			return AnnotationSet{}, err
		}
		reviewsByKey[review.Key()] = append(reviewsByKey[review.Key()], review)
	}
	set.Reviews = len(reviews)

	keys := make([]Key, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })

	for _, key := range keys {
		annotators := grouped[key]
		annotatorIDs := make([]string, 0, len(annotators))
		for annotatorID := range annotators {
			annotatorIDs = append(annotatorIDs, annotatorID)
		}
		sort.Strings(annotatorIDs)

		labels := make([]AnnotatorLabel, 0, len(annotatorIDs))
		distinct := make([]Label, 0, len(annotatorIDs))
		for _, annotatorID := range annotatorIDs {
			state := annotators[annotatorID]
			labels = append(labels, state.labels...)
			if !containsLabel(distinct, state.label) {
				distinct = append(distinct, state.label)
			}
		}

		resolved, ok, err := resolveReview(key, reviewsByKey[key], annotators, labels)
		if err != nil {
			return AnnotationSet{}, err
		}
		if ok {
			set.Truths = append(set.Truths, Truth{
				Key: key, Label: resolved.review.AdoptedLabel, Origin: TruthOriginResolvedReview,
				AnnotatorIDs: annotatorIDs, ReviewID: resolved.review.ReviewID,
				IndependentlyReviewed: resolved.independent, Labels: labels,
			})
			continue
		}
		if len(distinct) > 1 {
			// Disagreement without adjudication: keep every label, produce no
			// truth. Picking one would fabricate a human decision.
			set.Pending = append(set.Pending, PendingSample{Key: key, AnnotatorIDs: annotatorIDs, Labels: distinct})
			continue
		}
		origin := TruthOriginSingleBlindLabel
		if len(annotatorIDs) > 1 {
			origin = TruthOriginUnanimousBlind
		}
		set.Truths = append(set.Truths, Truth{
			Key: key, Label: distinct[0], Origin: origin,
			AnnotatorIDs: annotatorIDs, Labels: labels,
		})
	}
	return set, nil
}

type resolvedReview struct {
	review      Review
	independent bool
}

// resolveReview picks the resolved review for a key, if any. A self-review
// resolves the label but never counts as independent review.
func resolveReview(key Key, reviews []Review, annotators map[string]*annotatorState, labels []AnnotatorLabel) (resolvedReview, bool, error) {
	known := make(map[string]bool, len(labels))
	for _, label := range labels {
		known[label.AnnotationID] = true
	}
	candidates := make([]Review, 0, len(reviews))
	for _, review := range reviews {
		if !review.Resolved {
			continue
		}
		for _, sourceID := range review.SourceAnnotationIDs {
			if !known[sourceID] {
				return resolvedReview{}, false, Reject(ReasonInvalidAnnotation,
					"review %s references annotation %s which is not part of sample=%s",
					review.ReviewID, sourceID, key.SampleID)
			}
		}
		candidates = append(candidates, review)
	}
	if len(candidates) == 0 {
		return resolvedReview{}, false, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ReviewID < candidates[j].ReviewID })
	chosen := candidates[0]
	_, selfReview := annotators[chosen.ReviewerID]
	return resolvedReview{review: chosen, independent: !selfReview}, true, nil
}

func containsLabel(values []Label, want Label) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Summary renders a stable one-line description for CLI output.
func (s AnnotationSet) Summary() string {
	return fmt.Sprintf("annotation truth=%d pending=%d duplicates=%d reviews=%d",
		len(s.Truths), len(s.Pending), s.Duplicates, s.Reviews)
}
