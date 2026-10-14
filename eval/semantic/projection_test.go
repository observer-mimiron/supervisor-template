package semantic

import (
	"encoding/json"
	"strings"
	"testing"
)

func projectedKeys(t *testing.T, input JudgeInput) map[string]any {
	t.Helper()
	body, err := input.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("projection must be a JSON object: %v", err)
	}
	return decoded
}

// The projection is a whitelist, not a redaction pass: anything not named in it
// has no path to the wire.
func TestProjectionKeepsOnlyWhitelistFields(t *testing.T) {
	material := materialWithAnswer(t, "符合条件的合成客户共 12 位")
	input, err := Project(material)
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	decoded := projectedKeys(t, input)
	allowed := map[string]bool{"task_intent": true, "public_answer": true, "rubric": true, "reference": true}
	for key := range decoded {
		if !allowed[key] {
			t.Fatalf("projection carried a non-whitelist field %q", key)
		}
	}
	if len(decoded) != 3 {
		t.Fatalf("an omitted reference must not appear: %v", decoded)
	}
	body, err := input.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for name, value := range map[string]string{
		"sample id":       material.SampleID,
		"material digest": material.MaterialDigest,
		"verdict digest":  material.VerdictDigest,
		"dataset name":    material.DatasetName,
		"case id":         material.CaseID,
		"approval source": string(material.SourceApproval),
	} {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("the outbound body must not carry the material's %s (%q)", name, value)
		}
	}
	if !strings.Contains(text, RubricAnswerRelevanceV1[:24]) {
		t.Fatal("the frozen rubric must be part of the projection")
	}
}

// A nested non-whitelist field must be dropped even when it contains a known
// sensitive marker: it is not screened, it simply never becomes a candidate for
// transmission.
func TestProjectionDropsNestedNonWhitelistFields(t *testing.T) {
	material := materialWithAnswer(t, "符合条件的合成客户共 12 位")
	material.Reference = &Reference{Version: "v1-" + SensitiveCanaryMarker, Text: "已批准的外部参考事实"}
	material.Reference.Digest = DigestText(material.Reference.Text)
	material.MaterialDigest = MaterialDigest(material)
	if err := material.Validate(); err != nil {
		t.Fatalf("fixture material: %v", err)
	}
	input, err := Project(material)
	if err != nil {
		t.Fatalf("a marker in a non-whitelist nested field is dropped, not refused: %v", err)
	}
	body, err := input.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), SensitiveCanaryMarker) {
		t.Fatal("a nested non-whitelist field leaked into the outbound body")
	}
	if !strings.Contains(string(body), "已批准的外部参考事实") {
		t.Fatal("the whitelisted reference text must survive")
	}
}

func TestProjectionRefusesSensitiveWhitelistValues(t *testing.T) {
	cases := map[string]func(*Material){
		"task_intent":   func(m *Material) { m.TaskIntent = "查询 " + SensitiveCanaryMarker },
		"public_answer": func(m *Material) { m.PublicAnswer = "答复里有 " + SensitiveCanaryMarker },
		"reference": func(m *Material) {
			m.Reference = &Reference{Version: "1", Text: "参考 " + SensitiveCanaryMarker}
			m.Reference.Digest = DigestText(m.Reference.Text)
			m.MaterialDigest = MaterialDigest(*m)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			material := materialWithAnswer(t, "符合条件的合成客户共 12 位")
			mutate(&material)
			_, err := Project(material)
			if err == nil {
				t.Fatalf("a contaminated %s must refuse to be sent", name)
			}
			if ReasonOf(err) != ReasonSensitiveInput {
				t.Fatalf("expected sensitive_input, got %v", err)
			}
			// A safe failure must not re-publish the planted string.
			if strings.Contains(err.Error(), SensitiveCanaryMarker) &&
				!strings.Contains(err.Error(), `"`+SensitiveCanaryMarker+`"`) {
				t.Fatalf("the rejection must name the frozen marker, not quote the value: %v", err)
			}
		})
	}
}

// A clean projection is only evidence after the detector is proven able to find
// a planted marker. Both halves are required: a detector that matches nothing
// would call every material clean.
func TestCleanProjectionIsOnlyTrustedAfterTheMarkerProbe(t *testing.T) {
	if err := MarkerProbe(); err != nil {
		t.Fatalf("the sensitive-marker probe must detect its own planted marker: %v", err)
	}
	// Positive control: the same code path used by Project refuses a planted
	// marker.
	planted := materialWithAnswer(t, "答复 "+SensitiveCanaryMarker)
	if _, err := Project(planted); ReasonOf(err) != ReasonSensitiveInput {
		t.Fatalf("the probe's own input must be refused, got %v", err)
	}
	// Negative control: clean material projects.
	clean := materialWithAnswer(t, "符合条件的合成客户共 12 位")
	if _, err := Project(clean); err != nil {
		t.Fatalf("clean material must project: %v", err)
	}
	if marker, found := ContainsSensitiveMarker("符合条件的合成客户共 12 位"); found {
		t.Fatalf("ordinary material text must not match %q", marker)
	}
}

func TestProjectionRejectsOverLimitAndInvalidUTF8(t *testing.T) {
	huge := materialWithAnswer(t, "答复")
	huge.TaskIntent = strings.Repeat("意图", MaxProjectionFieldBytes)
	if _, err := Project(huge); ReasonOf(err) != ReasonInputTooLarge {
		t.Fatalf("expected input_too_large, got %v", err)
	}

	invalid := materialWithAnswer(t, "答复")
	invalid.PublicAnswer = string([]byte{0xff, 0xfe})
	if _, err := Project(invalid); err == nil {
		t.Fatal("invalid UTF-8 must be refused rather than replaced")
	}

	// The whole projection is bounded too, not only each field.
	combined := materialWithAnswer(t, "答复")
	combined.TaskIntent = strings.Repeat("a", MaxProjectionFieldBytes-1)
	combined.PublicAnswer = strings.Repeat("b", MaxProjectionFieldBytes-1)
	combined.Reference = &Reference{Version: "1", Text: strings.Repeat("c", MaxProjectionFieldBytes-1)}
	combined.Reference.Digest = DigestText(combined.Reference.Text)
	combined.MaterialDigest = MaterialDigest(combined)
	if _, err := Project(combined); ReasonOf(err) != ReasonInputTooLarge {
		t.Fatalf("expected the total size bound to apply, got %v", err)
	}
}

// The specification's contrast: identical hard evidence, only the public answer
// replaced. The input must be discriminative, otherwise no Judge could tell the
// two samples apart.
func TestProjectionDistinguishesTheContrastAnswers(t *testing.T) {
	relevant := materialWithAnswer(t, "符合沉睡客户筛选条件的合成客户共 12 位")
	irrelevant := materialWithAnswer(t, "今天适合选择绿色主题")
	if relevant.VerdictDigest != irrelevant.VerdictDigest || relevant.CaseID != irrelevant.CaseID {
		t.Fatal("the contrast pair must share hard evidence")
	}
	relevantInput, err := Project(relevant)
	if err != nil {
		t.Fatal(err)
	}
	irrelevantInput, err := Project(irrelevant)
	if err != nil {
		t.Fatal(err)
	}
	if relevantInput.Key() == irrelevantInput.Key() {
		t.Fatal("the two answers must project to different inputs")
	}
	if relevant.MaterialDigest == irrelevant.MaterialDigest {
		t.Fatal("changing only the answer must change the material identity")
	}
	left, _ := relevantInput.Bytes()
	right, _ := irrelevantInput.Bytes()
	if string(left) == string(right) {
		t.Fatal("the outbound bodies must differ")
	}
}

func TestProjectionIsDeterministicAndBounded(t *testing.T) {
	material := materialWithAnswer(t, "符合条件的合成客户共 12 位")
	first, err := Project(material)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Project(material)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := first.Bytes()
	right, _ := second.Bytes()
	if string(left) != string(right) {
		t.Fatal("the same material must project to the same bytes")
	}
	if len(left) > MaxProjectionTotalBytes {
		t.Fatalf("projection size %d exceeds the bound", len(left))
	}
	if first.Fields()[0] != "task_intent" {
		t.Fatalf("the whitelist order is part of the frozen contract: %v", first.Fields())
	}
}
