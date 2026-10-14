package semantic

import (
	"strings"
	"testing"
	"time"
)

func annotationFixture(id, annotator string, label Label, material Material) Annotation {
	return Annotation{
		SchemaVersion: AnnotationSchemaVersion, AnnotationID: id,
		SampleID: material.SampleID, MaterialDigest: material.MaterialDigest,
		Dimension: material.Dimension, Label: label, AnnotatorID: annotator,
		AnnotatedAtUTC: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC), Blind: true,
	}
}

func reviewFixture(id, reviewer string, label Label, resolved bool, material Material, sources ...string) Review {
	return Review{
		SchemaVersion: ReviewSchemaVersion, ReviewID: id,
		SampleID: material.SampleID, MaterialDigest: material.MaterialDigest,
		Dimension: material.Dimension, ReviewerID: reviewer,
		ReviewedAtUTC: time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC),
		AdoptedLabel:  label, SourceAnnotationIDs: sources, Resolved: resolved,
	}
}

func indexOf(t *testing.T, materials ...Material) MaterialIndex {
	t.Helper()
	index, err := IndexMaterials(materials)
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func TestResolveAnnotationsSingleBlindLabelIsTruth(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	set, err := ResolveAnnotations(indexOf(t, material),
		[]Annotation{annotationFixture("a-1", "annotator-1", LabelRelevant, material)}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(set.Truths) != 1 || set.Truths[0].Label != LabelRelevant {
		t.Fatalf("unexpected truths: %+v", set.Truths)
	}
	if set.Truths[0].Origin != TruthOriginSingleBlindLabel {
		t.Fatalf("a single label must not claim inter-annotator agreement: %+v", set.Truths[0])
	}
	if set.Truths[0].IndependentlyReviewed {
		t.Fatal("an unreviewed label is not independently reviewed")
	}
}

func TestResolveAnnotationsCollapsesIdenticalRepeatsAndRejectsConflicts(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	first := annotationFixture("a-1", "annotator-1", LabelRelevant, material)
	repeat := first
	repeat.AnnotationID = "a-2"
	set, err := ResolveAnnotations(indexOf(t, material), []Annotation{first, repeat}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// One person contributes one label: the repeat is not a second sample.
	if len(set.Truths) != 1 || len(set.Truths[0].AnnotatorIDs) != 1 {
		t.Fatalf("a repeat must not create a second sample: %+v", set.Truths)
	}
	if set.Duplicates != 1 {
		t.Fatalf("expected the repeat to be counted as a duplicate, got %d", set.Duplicates)
	}

	conflict := first
	conflict.AnnotationID = "a-3"
	conflict.Label = LabelIrrelevant
	if _, err := ResolveAnnotations(indexOf(t, material), []Annotation{first, conflict}, nil); err == nil {
		t.Fatal("one annotator giving two different labels must be rejected, not picked")
	} else if ReasonOf(err) != ReasonInvalidAnnotation {
		t.Fatalf("expected invalid_annotation, got %v", err)
	}
}

func TestResolveAnnotationsRetainsCrossAnnotatorDisagreementAndProducesNoTruth(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	relevant := annotationFixture("a-1", "annotator-1", LabelRelevant, material)
	irrelevant := annotationFixture("a-2", "annotator-2", LabelIrrelevant, material)
	set, err := ResolveAnnotations(indexOf(t, material), []Annotation{relevant, irrelevant}, nil)
	if err != nil {
		t.Fatalf("disagreement is retained, not an error: %v", err)
	}
	if len(set.Truths) != 0 {
		t.Fatalf("unresolved disagreement must not produce truth: %+v", set.Truths)
	}
	if len(set.Pending) != 1 || len(set.Pending[0].AnnotatorIDs) != 2 {
		t.Fatalf("expected one pending sample retaining both annotators: %+v", set.Pending)
	}
	if len(set.Pending[0].Labels) != 2 {
		t.Fatalf("both labels must be retained: %+v", set.Pending[0])
	}
}

func TestResolveAnnotationsRequiresResolvedReviewToAdjudicateDisagreement(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	relevant := annotationFixture("a-1", "annotator-1", LabelRelevant, material)
	irrelevant := annotationFixture("a-2", "annotator-2", LabelIrrelevant, material)

	// A review that is explicitly not resolved leaves the sample pending.
	pending := reviewFixture("r-1", "reviewer-1", LabelRelevant, false, material, "a-1", "a-2")
	set, err := ResolveAnnotations(indexOf(t, material), []Annotation{relevant, irrelevant}, []Review{pending})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(set.Truths) != 0 || len(set.Pending) != 1 {
		t.Fatalf("an unresolved review must not create truth: %+v", set)
	}

	resolved := reviewFixture("r-2", "reviewer-1", LabelIrrelevant, true, material, "a-1", "a-2")
	set, err = ResolveAnnotations(indexOf(t, material), []Annotation{relevant, irrelevant}, []Review{resolved})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(set.Pending) != 0 || len(set.Truths) != 1 {
		t.Fatalf("a resolved review must produce one truth: %+v", set)
	}
	truth := set.Truths[0]
	if truth.Label != LabelIrrelevant || truth.Origin != TruthOriginResolvedReview || truth.ReviewID != "r-2" {
		t.Fatalf("unexpected truth: %+v", truth)
	}
	if !truth.IndependentlyReviewed {
		t.Fatal("a review by someone who did not annotate is independent")
	}
	// The original labels stay visible after adjudication.
	if len(truth.Labels) != 2 {
		t.Fatalf("original labels must be preserved, got %+v", truth.Labels)
	}
}

func TestResolveAnnotationsSelfReviewIsNotIndependentReview(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	relevant := annotationFixture("a-1", "annotator-1", LabelRelevant, material)
	irrelevant := annotationFixture("a-2", "annotator-2", LabelIrrelevant, material)
	// annotator-1 reviews their own disagreement.
	self := reviewFixture("r-self", "annotator-1", LabelRelevant, true, material, "a-1", "a-2")
	set, err := ResolveAnnotations(indexOf(t, material), []Annotation{relevant, irrelevant}, []Review{self})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(set.Truths) != 1 {
		t.Fatalf("the review still adjudicates: %+v", set)
	}
	if set.Truths[0].IndependentlyReviewed {
		t.Fatal("a self-review must not be reported as independent review")
	}
}

func TestResolveAnnotationsTwoAnnotatorsAgreeingIsUnanimous(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	first := annotationFixture("a-1", "annotator-1", LabelRelevant, material)
	second := annotationFixture("a-2", "annotator-2", LabelRelevant, material)
	set, err := ResolveAnnotations(indexOf(t, material), []Annotation{first, second}, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(set.Truths) != 1 || set.Truths[0].Origin != TruthOriginUnanimousBlind {
		t.Fatalf("expected unanimous agreement: %+v", set.Truths)
	}
	if len(set.Truths[0].AnnotatorIDs) != 2 {
		t.Fatalf("both annotators must be retained: %+v", set.Truths[0])
	}
}

func TestResolveAnnotationsRejectsUnknownAndMismatchedMaterialReferences(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	unknown := annotationFixture("a-1", "annotator-1", LabelRelevant, material)
	unknown.MaterialDigest = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := ResolveAnnotations(indexOf(t, material), []Annotation{unknown}, nil); err == nil {
		t.Fatal("a label for a material that does not exist must be rejected")
	} else if ReasonOf(err) != ReasonInvalidAnnotation {
		t.Fatalf("expected invalid_annotation, got %v", err)
	}

	// Same sample ID, different answer text: the label must not carry over.
	otherAnswer := materialWithAnswer(t, "完全不同的答复")
	carried := annotationFixture("a-2", "annotator-1", LabelRelevant, material)
	if _, err := ResolveAnnotations(indexOf(t, otherAnswer), []Annotation{carried}, nil); err == nil {
		t.Fatal("a label must not be reusable for different answer text")
	}
}

func TestResolveAnnotationsRejectsReviewReferencingForeignAnnotations(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	annotation := annotationFixture("a-1", "annotator-1", LabelRelevant, material)
	foreign := reviewFixture("r-1", "reviewer-1", LabelRelevant, true, material, "a-does-not-exist")
	if _, err := ResolveAnnotations(indexOf(t, material), []Annotation{annotation}, []Review{foreign}); err == nil {
		t.Fatal("a review must reference annotations of the same material")
	}
}

func TestAnnotationAndReviewValidateClosedFields(t *testing.T) {
	material := materialWithAnswer(t, "答复")

	badLabel := annotationFixture("a-1", "annotator-1", Label("maybe"), material)
	if err := badLabel.Validate(); err == nil {
		t.Fatal("an unknown label must be rejected")
	}
	noAnnotator := annotationFixture("a-2", "", LabelRelevant, material)
	if err := noAnnotator.Validate(); err == nil {
		t.Fatal("an anonymous label without an annotator id must be rejected")
	}
	badDimension := annotationFixture("a-3", "annotator-1", LabelRelevant, material)
	badDimension.Dimension = "factual_correctness@1"
	if err := badDimension.Validate(); err == nil {
		t.Fatal("an unknown dimension must be rejected")
	}

	noSources := reviewFixture("r-1", "reviewer-1", LabelRelevant, true, material)
	if err := noSources.Validate(); err == nil {
		t.Fatal("a review without source annotations must be rejected")
	}
	badAdopted := reviewFixture("r-2", "reviewer-1", Label("maybe"), true, material, "a-1")
	if err := badAdopted.Validate(); err == nil {
		t.Fatal("an unknown adopted label must be rejected")
	}
}

func TestNotesAreBoundedAndScreenedLocally(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	ok := annotationFixture("a-1", "annotator-1", LabelRelevant, material)
	ok.Note = "边界样本：答复只覆盖了部分意图"
	if err := ok.Validate(); err != nil {
		t.Fatalf("an ordinary note must be accepted: %v", err)
	}

	planted := annotationFixture("a-2", "annotator-1", LabelRelevant, material)
	planted.Note = "凭证 " + SensitiveCanaryMarker
	err := planted.Validate()
	if err == nil {
		t.Fatal("a note carrying a known marker must be rejected")
	}
	if ReasonOf(err) != ReasonSensitiveInput {
		t.Fatalf("expected sensitive_input, got %v", err)
	}
	// The rejection must name the marker, never quote the surrounding content.
	if strings.Contains(err.Error(), "凭证") {
		t.Fatalf("rejection must not echo the rejected note: %v", err)
	}

	overLimit := annotationFixture("a-3", "annotator-1", LabelRelevant, material)
	overLimit.Note = strings.Repeat("长", MaxAnnotationNoteBytes+1)
	if reason := ReasonOf(overLimit.Validate()); reason != ReasonInputTooLarge {
		t.Fatalf("expected input_too_large, got %v", overLimit.Validate())
	}
}

func TestLoadAnnotationsRejectsStrictFileProblems(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	valid := `{"schema_version":"1","annotation_id":"a-1","sample_id":"` + material.SampleID +
		`","material_digest":"` + material.MaterialDigest + `","dimension":"answer_relevance@1",` +
		`"label":"relevant","annotator_id":"annotator-1","annotated_at_utc":"2026-10-09T12:00:00Z","blind":true}`
	path := writeTemp(t, "annotations.jsonl", valid+"\n")
	loaded, err := LoadAnnotations(path)
	if err != nil {
		t.Fatalf("valid annotation must load: %v", err)
	}
	if len(loaded) != 1 || loaded[0].Label != LabelRelevant {
		t.Fatalf("unexpected load: %+v", loaded)
	}

	duplicate := writeTemp(t, "duplicate-annotations.jsonl", valid+"\n"+valid+"\n")
	if _, err := LoadAnnotations(duplicate); err == nil {
		t.Fatal("a duplicated annotation id must be rejected")
	}

	unknown := writeTemp(t, "unknown-annotations.jsonl",
		strings.TrimSuffix(valid, "}")+`,"hard_passed":true}`+"\n")
	if _, err := LoadAnnotations(unknown); err == nil {
		t.Fatal("an unknown field must be rejected")
	}

	badLabel := writeTemp(t, "bad-label.jsonl", strings.Replace(valid, `"relevant"`, `"maybe"`, 1)+"\n")
	if _, err := LoadAnnotations(badLabel); err == nil {
		t.Fatal("an unknown label must be rejected at load time")
	}
}

func TestLoadReviewsRejectsStrictFileProblems(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	valid := `{"schema_version":"1","review_id":"r-1","sample_id":"` + material.SampleID +
		`","material_digest":"` + material.MaterialDigest + `","dimension":"answer_relevance@1",` +
		`"reviewer_id":"reviewer-1","reviewed_at_utc":"2026-10-09T13:00:00Z","adopted_label":"relevant",` +
		`"source_annotation_ids":["a-1"],"resolved":true}`
	path := writeTemp(t, "reviews.jsonl", valid+"\n")
	loaded, err := LoadReviews(path)
	if err != nil {
		t.Fatalf("valid review must load: %v", err)
	}
	if len(loaded) != 1 || !loaded[0].Resolved {
		t.Fatalf("unexpected load: %+v", loaded)
	}
	if _, err := LoadReviews(writeTemp(t, "bad-review.jsonl",
		strings.Replace(valid, `"source_annotation_ids":["a-1"]`, `"source_annotation_ids":[]`, 1)+"\n")); err == nil {
		t.Fatal("a review without sources must be rejected at load time")
	}
}
