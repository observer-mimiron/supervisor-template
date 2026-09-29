package eval

import "time"

// Dataset is the immutable, human-maintained evaluation envelope.
type Dataset struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Cases       []Case `json:"cases"`
}

type Case struct {
	ID                   string           `json:"id"`
	Version              string           `json:"version"`
	Category             string           `json:"category"`
	RiskLevel            string           `json:"risk_level"`
	ImpactTags           []string         `json:"impact_tags"`
	BusinessGoal         string           `json:"business_goal"`
	Preconditions        Preconditions    `json:"preconditions"`
	RequestSteps         []RequestStep    `json:"request_steps"`
	ExpectedResults      ExpectedResults  `json:"expected_results"`
	ForbiddenEffects     ForbiddenEffects `json:"forbidden_side_effects"`
	EvidenceRequirements []string         `json:"evidence_requirements"`
	EvaluatorRules       []string         `json:"evaluator_rules"`
	Timeout              string           `json:"timeout"`
	RetryBudget          int              `json:"retry_budget"`
	IdempotencyKey       string           `json:"idempotency_key"`
	CleanupPolicy        string           `json:"cleanup_policy"`
}

type Preconditions struct {
	Fixture string `json:"fixture"`
	Subject string `json:"subject"`
}

type RequestStep struct {
	Action         string `json:"action"`
	Message        string `json:"message,omitempty"`
	ConversationID string `json:"conversation_id,omitempty"`
	Decision       string `json:"decision,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	Subject        string `json:"subject,omitempty"`
}

type ExpectedResults struct {
	Terminal   string         `json:"terminal"`
	EventTypes []string       `json:"event_types"`
	Result     map[string]any `json:"result,omitempty"`
}

type ForbiddenEffects struct {
	ToolIDs   []string `json:"tool_ids"`
	MaxWrites int      `json:"max_writes"`
}

type Duration struct{ time.Duration }

// RequiredEvidence returns the minimum evidence contract for a Case. The
// dataset may request additional evidence, but it cannot opt out of these
// invariants.
func RequiredEvidence(item Case) []string {
	seen := make(map[string]bool)
	add := func(requirement string) {
		if !seen[requirement] {
			seen[requirement] = true
		}
	}
	add("http_statuses")
	add("trace_correlation")
	add("cleanup")
	if item.ExpectedResults.Terminal != "request_error" {
		add("events")
		add("sequence")
		add("run_state")
	}
	for _, eventType := range item.ExpectedResults.EventTypes {
		if eventType == "tool_call" {
			add("tool_calls")
		}
		if eventType == "approval_required" {
			add("approval")
		}
	}
	for _, step := range item.RequestSteps {
		if step.Action == "approval" {
			add("approval")
		}
	}
	order := []string{"events", "tool_calls", "run_state", "trace_correlation", "approval", "cleanup", "http_statuses", "sequence"}
	result := make([]string, 0, len(seen))
	for _, requirement := range order {
		if seen[requirement] {
			result = append(result, requirement)
		}
	}
	return result
}
