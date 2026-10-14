package semantic

import (
	"encoding/json"
	"fmt"
)

// The Judge input is rebuilt from exactly four bounded scalar strings. The
// material, the report and the evidence are never serialized into a request:
// what is not in the whitelist cannot leak, and a nested object inside a
// non-whitelist field has no path to the wire at all.

// JudgeInput is the complete, frozen whitelist of Judge input fields.
//
// Reference is optional and empty by default: the first dimension does not need
// an external fact, and adding one changes the material identity.
type JudgeInput struct {
	TaskIntent   string `json:"task_intent"`
	PublicAnswer string `json:"public_answer"`
	Rubric       string `json:"rubric"`
	Reference    string `json:"reference,omitempty"`
}

// Key identifies a projected input for deterministic test doubles and for
// reporting, without carrying the material identity to the provider.
func (i JudgeInput) Key() string {
	return i.TaskIntent + "\x00" + i.PublicAnswer
}

// Fields returns the whitelist as an ordered list.
func (i JudgeInput) Fields() []string { return append([]string(nil), ProjectionFields...) }

// Bytes is the canonical JSON body that would be sent. Its size is bounded by
// the projection limits, so it is also what the budget check measures.
func (i JudgeInput) Bytes() ([]byte, error) {
	var buffer []byte
	buffer, err := json.Marshal(i)
	if err != nil {
		return nil, err
	}
	return buffer, nil
}

// Project rebuilds the Judge input from a material.
//
// Only whitelist values are read. Everything else the material carries —
// identities, digests, dataset/case names, approval source, notes, and any
// nested non-whitelist field — is dropped rather than screened, because it is
// never a candidate for transmission.
func Project(material Material) (JudgeInput, error) {
	input := JudgeInput{
		TaskIntent:   material.TaskIntent,
		PublicAnswer: material.PublicAnswer,
		Rubric:       RubricAnswerRelevanceV1,
	}
	if material.Reference != nil {
		input.Reference = material.Reference.Text
	}
	if err := input.Validate(); err != nil {
		return JudgeInput{}, err
	}
	return input, nil
}

// Validate screens every whitelist value: valid UTF-8, within the registered
// size bound, and free of known sensitive markers.
//
// Over-limit material is refused instead of truncated, and a contaminated
// whitelist value is refused without substituting a "clean" version: either
// would silently change the task being judged.
func (i JudgeInput) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"task_intent", i.TaskIntent},
		{"public_answer", i.PublicAnswer},
		{"rubric", i.Rubric},
		{"reference", i.Reference},
	} {
		if err := checkBounded(field.name, field.value); err != nil {
			return err
		}
		if marker, found := ContainsSensitiveMarker(field.value); found {
			// The rejection names the frozen marker and the field; it never
			// echoes the surrounding value.
			return Reject(ReasonSensitiveInput, "%s contains the known marker %q", field.name, marker)
		}
	}
	body, err := i.Bytes()
	if err != nil {
		return Reject(ReasonInvalidMaterial, "projection could not be encoded")
	}
	if len(body) > MaxProjectionTotalBytes {
		return Reject(ReasonInputTooLarge, "projection exceeds %d bytes", MaxProjectionTotalBytes)
	}
	return nil
}

// MarkerProbe is the positive control for the sensitive-marker screen: it proves
// the detector can find a known planted marker before a clean result is trusted.
//
// A scanner that silently matches nothing would report every material as clean,
// so "nothing was found" only becomes evidence after this returns nil.
func MarkerProbe() error {
	probe := JudgeInput{
		TaskIntent:   "probe intent",
		PublicAnswer: "planted " + SensitiveCanaryMarker,
		Rubric:       RubricAnswerRelevanceV1,
	}
	err := probe.Validate()
	if err == nil {
		return fmt.Errorf("sensitive-marker probe did not detect the planted marker")
	}
	if ReasonOf(err) != ReasonSensitiveInput {
		return fmt.Errorf("sensitive-marker probe reported %s instead of sensitive_input", ReasonOf(err))
	}
	return nil
}
