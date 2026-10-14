package semantic

import (
	"strings"
	"testing"
	"time"
)

func sampleSpec() MaterialSpec {
	return MaterialSpec{
		SchemaVersion:   MaterialSchemaVersion,
		SampleID:        "sample-1",
		MaterialGroupID: "group-1",
		DatasetName:     "synthetic-operations",
		DatasetVersion:  "5",
		CaseID:          "positive-audience-query",
		CaseVersion:     "1.0.0",
		VerdictDigest:   "hard-verdict-digest-1",
		Dimension:       DimensionAnswerRelevanceValue,
		IntentSource:    IntentSourceReviewedBusinessGoal,
		TaskIntent:      "查询符合沉睡客户条件的合成客户数量",
		SourceApproval:  SourceApprovalSynthetic,
		Applicability:   ApplicabilityApplicable,
	}
}

func materialWithAnswer(t *testing.T, answer string) Material {
	t.Helper()
	material := Material{
		MaterialSpec: sampleSpec(),
		PublicAnswer: answer,
		Rubric:       FrozenRubricIdentity(),
		Projection:   FrozenProjectionIdentity(),
	}
	material.MaterialDigest = MaterialDigest(material)
	if err := material.Validate(); err != nil {
		t.Fatalf("fixture material must be valid: %v", err)
	}
	return material
}

// The hard verdict is not the material identity: two answers with the same hard
// events must be two different materials, and a whitespace-only edit is a
// different answer too.
func TestMaterialDigestTracksExactAnswerBytes(t *testing.T) {
	relevant := materialWithAnswer(t, "符合沉睡客户筛选条件的合成客户共 12 位")
	irrelevant := materialWithAnswer(t, "今天适合选择绿色主题")
	if relevant.MaterialDigest == irrelevant.MaterialDigest {
		t.Fatal("changing only the public answer must change the material digest")
	}
	// Identical hard identity in both, so the difference is the answer alone.
	if relevant.VerdictDigest != irrelevant.VerdictDigest || relevant.CaseID != irrelevant.CaseID {
		t.Fatal("fixtures must share the same hard evidence")
	}

	spaced := materialWithAnswer(t, "符合沉睡客户筛选条件的合成客户共 12 位 ")
	if spaced.MaterialDigest == relevant.MaterialDigest {
		t.Fatal("an exact-bytes change must change the digest")
	}

	same := materialWithAnswer(t, "符合沉睡客户筛选条件的合成客户共 12 位")
	if same.MaterialDigest != relevant.MaterialDigest {
		t.Fatal("the same material must keep the same digest across loads")
	}
}

func TestMaterialDigestCoversIntentRubricReferenceAndIdentity(t *testing.T) {
	base := materialWithAnswer(t, "答复")

	otherIntent := base
	otherIntent.TaskIntent = "另一个任务意图"
	otherIntent.MaterialDigest = MaterialDigest(otherIntent)
	if otherIntent.MaterialDigest == base.MaterialDigest {
		t.Fatal("changing the task intent must change the digest")
	}

	otherCase := base
	otherCase.CaseVersion = "1.0.1"
	otherCase.MaterialDigest = MaterialDigest(otherCase)
	if otherCase.MaterialDigest == base.MaterialDigest {
		t.Fatal("changing the Case identity must change the digest")
	}

	otherVerdict := base
	otherVerdict.VerdictDigest = "hard-verdict-digest-2"
	otherVerdict.MaterialDigest = MaterialDigest(otherVerdict)
	if otherVerdict.MaterialDigest == base.MaterialDigest {
		t.Fatal("changing the hard verdict digest must change the digest")
	}

	withReference := base
	withReference.Reference = &Reference{Version: "1", Text: "外部事实参考"}
	withReference.Reference.Digest = DigestText(withReference.Reference.Text)
	withReference.MaterialDigest = MaterialDigest(withReference)
	if withReference.MaterialDigest == base.MaterialDigest {
		t.Fatal("adding a reference must change the digest")
	}
}

// Rebuilding from a re-run of the same Case must not change material identity:
// trace IDs and elapsed time are not part of what was judged.
func TestMaterialDigestIgnoresVolatileRunDetail(t *testing.T) {
	spec := sampleSpec()
	first, err := BuildMaterial(spec, []PublicEvent{
		{Type: "started", Data: map[string]string{"conversation_id": "c-1"}},
		{Type: "text", Data: map[string]string{"content": "符合条件的合成客户共 12 位"}},
		{Type: "completed", Data: nil},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	second, err := BuildMaterial(spec, []PublicEvent{
		{Type: "started", Data: map[string]string{"conversation_id": "c-2", "trace_id": "another-trace"}},
		{Type: "progress", Data: map[string]string{"elapsed_ms": "999"}},
		{Type: "text", Data: map[string]string{"content": "符合条件的合成客户共 12 位"}},
		{Type: "completed", Data: nil},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if first.MaterialDigest != second.MaterialDigest {
		t.Fatal("volatile run detail must not change material identity")
	}
}

func TestMaterialMissingAnswerIsMaterialMissingNotNotApplicable(t *testing.T) {
	material := Material{
		MaterialSpec: sampleSpec(),
		PublicAnswer: "",
		Rubric:       FrozenRubricIdentity(),
		Projection:   FrozenProjectionIdentity(),
	}
	material.MaterialDigest = MaterialDigest(material)
	err := material.Validate()
	if err == nil {
		t.Fatal("an applicable material without an answer must be rejected")
	}
	if reason := ReasonOf(err); reason != ReasonMaterialMissing {
		t.Fatalf("expected material_missing, got %s", reason)
	}
}

func TestBuildMaterialNeverSynthesizesAMissingAnswer(t *testing.T) {
	_, err := BuildMaterial(sampleSpec(), []PublicEvent{{Type: "completed"}})
	if err == nil {
		t.Fatal("expected a missing public answer to be rejected")
	}
	if reason := ReasonOf(err); reason != ReasonMaterialMissing {
		t.Fatalf("expected material_missing, got %s", reason)
	}
	// A trailing empty text event means there is no usable answer, and the
	// builder must not fall back to an earlier event.
	_, err = BuildMaterial(sampleSpec(), []PublicEvent{
		{Type: "text", Data: map[string]string{"content": "较早的中间输出"}},
		{Type: "text", Data: map[string]string{"content": "   "}},
	})
	if reason := ReasonOf(err); reason != ReasonMaterialMissing {
		t.Fatalf("expected material_missing for an empty final answer, got %v", err)
	}
}

func TestAnswerFromEventsUsesTheLastPublicTextEvent(t *testing.T) {
	answer, ok := AnswerFromEvents([]PublicEvent{
		{Type: "decision", Data: map[string]string{"tool_id": "user_query"}},
		{Type: "text", Data: map[string]string{"content": "第一段"}},
		{Type: "text", Data: map[string]string{"content": "最终答复"}},
	})
	if !ok || answer != "最终答复" {
		t.Fatalf("expected the last text event, got %q ok=%v", answer, ok)
	}
	if _, ok := AnswerFromEvents([]PublicEvent{{Type: "completed"}}); ok {
		t.Fatal("a run without a text event has no public answer")
	}
	if _, ok := AnswerFromEvents(nil); ok {
		t.Fatal("no events means no public answer")
	}
}

func TestMaterialNotApplicableRequiresTheFrozenReasonAndNoAnswer(t *testing.T) {
	build := func(applicability Applicability, reason NotApplicableReason, answer string) Material {
		material := Material{
			MaterialSpec: sampleSpec(),
			PublicAnswer: answer,
			Rubric:       FrozenRubricIdentity(),
			Projection:   FrozenProjectionIdentity(),
		}
		material.Applicability = applicability
		material.NotApplicableReason = reason
		material.MaterialDigest = MaterialDigest(material)
		return material
	}

	ok := build(ApplicabilityNotApplicable, NotApplicableReasonNoAnswerRequired, "")
	if err := ok.Validate(); err != nil {
		t.Fatalf("a contract-declared not-applicable sample must be valid: %v", err)
	}

	// "The model chose not to answer" is not a sample contract.
	unknown := build(ApplicabilityNotApplicable, NotApplicableReason("model_decided"), "")
	if reason := ReasonOf(unknown.Validate()); reason != ReasonInvalidMaterial {
		t.Fatalf("expected invalid_material for an unknown not-applicable reason, got %v", unknown.Validate())
	}

	missingReason := build(ApplicabilityNotApplicable, "", "")
	if err := missingReason.Validate(); err == nil {
		t.Fatal("not applicable requires the closed reason")
	}

	// An available answer cannot be excused from evaluation.
	withAnswer := build(ApplicabilityNotApplicable, NotApplicableReasonNoAnswerRequired, "这里有答复")
	if err := withAnswer.Validate(); err == nil {
		t.Fatal("not-applicable material must not carry a public answer")
	}
}

func TestMaterialRequiresEveryDeclaredField(t *testing.T) {
	cases := map[string]func(*Material){
		"sample_id":         func(m *Material) { m.SampleID = "" },
		"material_group_id": func(m *Material) { m.MaterialGroupID = "" },
		"dataset_name":      func(m *Material) { m.DatasetName = "" },
		"dataset_version":   func(m *Material) { m.DatasetVersion = "" },
		"case_id":           func(m *Material) { m.CaseID = "" },
		"case_version":      func(m *Material) { m.CaseVersion = "" },
		"verdict_digest":    func(m *Material) { m.VerdictDigest = "" },
		"task_intent":       func(m *Material) { m.TaskIntent = "" },
		"schema_version":    func(m *Material) { m.SchemaVersion = "2" },
		"dimension":         func(m *Material) { m.Dimension = "factual_correctness@1" },
		"intent_source":     func(m *Material) { m.IntentSource = "unreviewed_guess" },
		"source_approval":   func(m *Material) { m.SourceApproval = "real_user_trace" },
		"applicability":     func(m *Material) { m.Applicability = "maybe" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			material := materialWithAnswer(t, "答复")
			mutate(&material)
			if err := material.Validate(); err == nil {
				t.Fatalf("expected a missing/invalid %s to be rejected", name)
			}
		})
	}
}

func TestMaterialRejectsIdentityMismatchAndTampering(t *testing.T) {
	base := materialWithAnswer(t, "答复")

	wrongRubric := base
	wrongRubric.Rubric.Digest = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := wrongRubric.Validate(); err == nil {
		t.Fatal("a rubric identity that is not the frozen rubric must be rejected")
	}

	wrongProjection := base
	wrongProjection.Projection.Version = "2"
	if err := wrongProjection.Validate(); err == nil {
		t.Fatal("a projection identity mismatch must be rejected")
	}

	// Editing the answer without recomputing the digest is exactly the mistake
	// that would let an old label be applied to new text.
	tampered := base
	tampered.PublicAnswer = "换了一段完全不同的答复"
	if err := tampered.Validate(); err == nil {
		t.Fatal("a material whose digest does not match its content must be rejected")
	}

	missingDigest := base
	missingDigest.MaterialDigest = ""
	if err := missingDigest.Validate(); err == nil {
		t.Fatal("a material without its own digest must be rejected")
	}
}

func TestMaterialReferenceIdentityIsBoundToItsText(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	material.Reference = &Reference{Version: "1", Text: "参考事实", Digest: "not-the-digest"}
	material.MaterialDigest = MaterialDigest(material)
	if err := material.Validate(); err == nil {
		t.Fatal("a reference digest that does not match its text must be rejected")
	}

	material.Reference.Digest = DigestText(material.Reference.Text)
	material.MaterialDigest = MaterialDigest(material)
	if err := material.Validate(); err != nil {
		t.Fatalf("a consistent reference must be accepted: %v", err)
	}

	// Changing the reference text must change material identity even though the
	// hard verdict and the answer stayed the same.
	changed := material
	changed.Reference = &Reference{Version: "2", Text: "改过的参考事实"}
	changed.Reference.Digest = DigestText(changed.Reference.Text)
	changed.MaterialDigest = MaterialDigest(changed)
	if changed.MaterialDigest == material.MaterialDigest {
		t.Fatal("changing the reference must change the material identity")
	}
}

func TestMaterialRejectsOverLimitAndInvalidUTF8(t *testing.T) {
	// Built without the validating helper on purpose: this is the case the
	// helper exists to refuse.
	raw := func(answer string) Material {
		material := Material{
			MaterialSpec: sampleSpec(), PublicAnswer: answer,
			Rubric: FrozenRubricIdentity(), Projection: FrozenProjectionIdentity(),
		}
		material.MaterialDigest = MaterialDigest(material)
		return material
	}
	over := raw(strings.Repeat("很长的答复", MaxProjectionFieldBytes))
	if reason := ReasonOf(over.Validate()); reason != ReasonInputTooLarge {
		t.Fatalf("expected input_too_large, got %v", over.Validate())
	}
	// Over-limit material is refused, never truncated into a different task.
	if !strings.Contains(over.Validate().Error(), "exceeds") {
		t.Fatalf("expected an explicit size rejection, got %v", over.Validate())
	}

	invalid := raw(string([]byte{0xff, 0xfe, 0xfd}))
	if err := invalid.Validate(); err == nil {
		t.Fatal("invalid UTF-8 material must be rejected, not silently replaced")
	}
}

func TestLoadMaterialsRejectsDuplicatesAndUnknownFields(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	line := `{"schema_version":"1","sample_id":"sample-1","material_group_id":"group-1",` +
		`"dataset_name":"synthetic-operations","dataset_version":"5","case_id":"positive-audience-query",` +
		`"case_version":"1.0.0","verdict_digest":"hard-verdict-digest-1","dimension":"answer_relevance@1",` +
		`"intent_source":"reviewed_case_business_goal","task_intent":"查询符合沉睡客户条件的合成客户数量",` +
		`"source_approval":"synthetic","applicability":"applicable","public_answer":"答复",` +
		`"rubric":{"id":"` + material.Rubric.ID + `","version":"` + material.Rubric.Version + `","digest":"` + material.Rubric.Digest + `"},` +
		`"projection":{"id":"` + material.Projection.ID + `","version":"` + material.Projection.Version + `","digest":"` + material.Projection.Digest + `"},` +
		`"material_digest":"` + material.MaterialDigest + `"}`
	path := writeTemp(t, "materials.jsonl", line+"\n")
	loaded, err := LoadMaterials(path)
	if err != nil {
		t.Fatalf("valid material line must load: %v", err)
	}
	if len(loaded) != 1 || loaded[0].MaterialDigest != material.MaterialDigest {
		t.Fatalf("unexpected load result: %+v", loaded)
	}

	duplicate := writeTemp(t, "duplicate-materials.jsonl", line+"\n"+line+"\n")
	if _, err := LoadMaterials(duplicate); err == nil {
		t.Fatal("the same material twice would double its statistical weight")
	}

	unknown := writeTemp(t, "unknown-materials.jsonl", strings.TrimSuffix(line, "}")+`,"hard_passed":true}`+"\n")
	if _, err := LoadMaterials(unknown); err == nil {
		t.Fatal("an unknown field must be rejected")
	}

	invalid := writeTemp(t, "invalid-materials.jsonl", strings.Replace(line, `"applicable"`, `"maybe"`, 1)+"\n")
	if _, err := LoadMaterials(invalid); err == nil {
		t.Fatal("an invalid material must be rejected at load time")
	}
}

func TestMaterialIndexRejectsUnknownAndMismatchedReferences(t *testing.T) {
	material := materialWithAnswer(t, "答复")
	index, err := IndexMaterials([]Material{material})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Lookup(material.Key()); err != nil {
		t.Fatalf("a known key must resolve: %v", err)
	}
	// Same sample ID, different answer: this is a different material, and an old
	// label must not silently attach to it.
	otherAnswer := materialWithAnswer(t, "另一段答复")
	if _, err := index.Lookup(otherAnswer.Key()); err == nil {
		t.Fatal("a sample ID match with a different material digest must not resolve")
	}
	unknown := Key{SampleID: "sample-does-not-exist", MaterialDigest: "abc", Dimension: DimensionAnswerRelevanceValue}
	if _, err := index.Lookup(unknown); err == nil {
		t.Fatal("an unknown material reference must be rejected")
	}
}

func TestFrozenIdentitiesAreStableAndDistinct(t *testing.T) {
	rubric := FrozenRubricIdentity()
	projection := FrozenProjectionIdentity()
	markers := FrozenSensitiveMarkerSetIdentity()
	if rubric.Digest == "" || projection.Digest == "" || markers.Digest == "" {
		t.Fatal("frozen identities must have digests")
	}
	if rubric.Digest == projection.Digest {
		t.Fatal("rubric and projection identities must differ")
	}
	if _, found := ContainsSensitiveMarker(SensitiveCanaryMarker); !found {
		t.Fatal("the canary must be detectable, otherwise a clean probe proves nothing")
	}
	if marker, found := ContainsSensitiveMarker("普通答复文本"); found {
		t.Fatalf("ordinary answer text must not look sensitive, matched %q", marker)
	}
}

func TestAnnotationTimestampsAreUTCBound(t *testing.T) {
	annotation := Annotation{
		SchemaVersion: AnnotationSchemaVersion, AnnotationID: "a-1", SampleID: "sample-1",
		MaterialDigest: "digest", Dimension: DimensionAnswerRelevanceValue, Label: LabelRelevant,
		AnnotatorID: "annotator-1", AnnotatedAtUTC: time.Time{},
	}
	if err := annotation.Validate(); err == nil {
		t.Fatal("an annotation without a timestamp must be rejected")
	}
}
