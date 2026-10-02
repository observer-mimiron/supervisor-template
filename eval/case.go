// Package eval defines the versioned, human-reviewed data contracts for local
// project feature acceptance: the Dataset/Case envelope, the Selection used by
// the PR gate, the EvaluationProfile that labels how evidence was produced, and
// the failure/feedback records a report carries.
//
// The package owns parsing, validation and description of evaluation input only.
// It does not execute Cases, collect evidence, or decide pass/fail: execution
// belongs to eval/runner and deterministic assertions belong to eval/evaluator.
// It owns no Runtime state, Policy, Approval, Idempotency, Tool or terminal-event
// authority, and it must stay free of HTTP, model, database and MCP dependencies.
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
	Postconditions       Postconditions   `json:"postconditions,omitempty"`
	Timeout              string           `json:"timeout"`
	RetryBudget          int              `json:"retry_budget"`
	IdempotencyKey       string           `json:"idempotency_key"`
	CleanupPolicy        string           `json:"cleanup_policy"`
}

// Postconditions are optional deterministic checks that run after the public
// API flow. They describe only bounded summaries, never raw SQL or records.
type Postconditions struct {
	Database *DatabasePostcondition `json:"database,omitempty"`
	Logs     *LogPostcondition      `json:"logs,omitempty"`
}

type DatabasePostcondition struct {
	Backend          string                     `json:"backend"`
	BeforeOrderCount *int                       `json:"before_order_count,omitempty"`
	AfterOrderCount  *int                       `json:"after_order_count,omitempty"`
	ExpectedOrders   []DatabaseOrderExpectation `json:"expected_orders,omitempty"`
}

type DatabaseOrderExpectation struct {
	UserID      uint64 `json:"user_id"`
	ProductID   uint64 `json:"product_id"`
	Quantity    int64  `json:"quantity"`
	TotalAmount string `json:"total_amount"`
}

type LogPostcondition struct {
	MinRecords         int      `json:"min_records,omitempty"`
	RequiredPhases     []string `json:"required_phases,omitempty"`
	RequiredErrorCodes []string `json:"required_error_codes,omitempty"`
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
	// CancelAfterMS is required for the chat_cancel action: how long to wait
	// before cancelling, measured from the moment the chat request is sent. It
	// must fall inside the execution window, which is as long as the Tool delay
	// configured through tools.*.fake_delay_ms; a value outside it makes the Run
	// complete instead of being cancelled, so the Case fails loudly.
	CancelAfterMS int `json:"cancel_after_ms,omitempty"`
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
	if item.Postconditions.Database != nil {
		add("database_state")
	}
	if item.Postconditions.Logs != nil {
		add("diagnostic_logs")
	}
	order := []string{"events", "tool_calls", "run_state", "database_state", "diagnostic_logs", "trace_correlation", "approval", "cleanup", "http_statuses", "sequence"}
	result := make([]string, 0, len(seen))
	for _, requirement := range order {
		if seen[requirement] {
			result = append(result, requirement)
		}
	}
	return result
}
