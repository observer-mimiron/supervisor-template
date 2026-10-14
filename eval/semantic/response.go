package semantic

// Judge output is a closed label plus a closed reason code. Free-form
// explanations, self-reported confidence and unknown fields are refused: none of
// them is a trustworthy measurement, and keeping them would put unvalidated
// model text on the path to a report.

// JudgeReason is the closed set of response reason codes.
type JudgeReason string

// Closed response reason codes.
const (
	JudgeReasonAddressesIntent        JudgeReason = "addresses_intent"
	JudgeReasonOffTopic               JudgeReason = "off_topic"
	JudgeReasonGenericAcknowledgement JudgeReason = "generic_acknowledgement"
	JudgeReasonNotApplicable          JudgeReason = "not_applicable"
)

// JudgeResponse is the only accepted provider output.
type JudgeResponse struct {
	Label  Label       `json:"label"`
	Reason JudgeReason `json:"reason_code"`
}

// ValidJudgeReason reports whether the value is in the closed reason set.
func ValidJudgeReason(value JudgeReason) bool {
	switch value {
	case JudgeReasonAddressesIntent, JudgeReasonOffTopic, JudgeReasonGenericAcknowledgement, JudgeReasonNotApplicable:
		return true
	}
	return false
}

// JudgeReasons returns the closed reason set in a stable order.
func JudgeReasons() []JudgeReason {
	return []JudgeReason{
		JudgeReasonAddressesIntent, JudgeReasonOffTopic,
		JudgeReasonGenericAcknowledgement, JudgeReasonNotApplicable,
	}
}

// consistentPair is the frozen label/reason contract. A label with a reason that
// does not belong to it is an invalid output, not a preference to be resolved.
func consistentPair(label Label, reason JudgeReason) bool {
	switch label {
	case LabelRelevant:
		return reason == JudgeReasonAddressesIntent
	case LabelIrrelevant:
		return reason == JudgeReasonOffTopic || reason == JudgeReasonGenericAcknowledgement
	case LabelNotApplicable:
		return reason == JudgeReasonNotApplicable
	}
	return false
}

// Validate checks the closed label/reason contract.
func (r JudgeResponse) Validate() error {
	if !ValidLabel(r.Label) {
		return Reject(ReasonInvalidOutput, "judge response carries an unknown label")
	}
	if !ValidJudgeReason(r.Reason) {
		return Reject(ReasonInvalidOutput, "judge response carries an unknown reason code")
	}
	if !consistentPair(r.Label, r.Reason) {
		return Reject(ReasonInvalidOutput, "judge response label and reason code are inconsistent")
	}
	return nil
}

// ParseJudgeResponse validates raw provider output into a JudgeResponse.
//
// The raw bytes are discarded afterwards: an invalid response is never stored,
// never uploaded and never shown as a diagnostic, because a free-form model
// reply is exactly what must not become an artifact.
func ParseJudgeResponse(raw []byte) (JudgeResponse, error) {
	var response JudgeResponse
	if err := decodeStrict(raw, &response); err != nil {
		return JudgeResponse{}, Reject(ReasonInvalidOutput, "judge response is not a strict JSON object: %v", err)
	}
	if err := response.Validate(); err != nil {
		return JudgeResponse{}, err
	}
	return response, nil
}
