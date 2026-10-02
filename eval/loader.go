package eval

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

var allowedCategories = map[string]bool{"positive": true, "negative": true, "boundary": true, "diversity": true}
var allowedRisks = map[string]bool{"low": true, "medium": true, "high": true}

// chat_cancel 在 Run 仍在执行时发出取消：只有这一步能证明 cancel signal 真的接线。
var allowedActions = map[string]bool{"chat": true, "chat_cancel": true, "approval": true, "resume": true, "cancel": true, "repeat": true}
var allowedEvaluators = map[string]bool{"business_correctness@1": true, "architecture_boundary@1": true, "side_effect_safety@1": true, "stability@1": true}
var requiredEvaluators = []string{"business_correctness@1", "architecture_boundary@1", "side_effect_safety@1", "stability@1"}
var allowedEvidence = map[string]bool{"events": true, "tool_calls": true, "run_state": true, "database_state": true, "diagnostic_logs": true, "trace_correlation": true, "approval": true, "cleanup": true, "http_statuses": true, "sequence": true}
var allowedTerminals = map[string]bool{"completed": true, "failed": true, "canceled": true, "request_error": true}
var allowedEventTypes = map[string]bool{"started": true, "decision": true, "plan": true, "progress": true, "tool_call": true, "approval_required": true, "reconciliation_required": true, "text": true, "completed": true, "failed": true, "canceled": true}

// Load decodes a dataset and rejects duplicate object keys and unknown fields.
func Load(path string) (Dataset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Dataset{}, err
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return Dataset{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var dataset Dataset
	if err := decoder.Decode(&dataset); err != nil {
		return Dataset{}, fmt.Errorf("decode dataset: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Dataset{}, errors.New("dataset has trailing JSON")
	}
	if err := validateDataset(dataset); err != nil {
		return Dataset{}, err
	}
	return dataset, nil
}

func validateDataset(dataset Dataset) error {
	if strings.TrimSpace(dataset.Name) == "" || strings.TrimSpace(dataset.Version) == "" || len(dataset.Cases) == 0 {
		return errors.New("dataset requires name, version and cases")
	}
	seen := map[string]bool{}
	for _, item := range dataset.Cases {
		if item.ID == "" || item.Version == "" || seen[item.ID+"@"+item.Version] {
			return fmt.Errorf("duplicate or missing case id/version: %s@%s", item.ID, item.Version)
		}
		seen[item.ID+"@"+item.Version] = true
		if containsUnsafeText(item) {
			return fmt.Errorf("case %q contains dynamic, secret-like or URL text", item.ID)
		}
		if !allowedCategories[item.Category] || !allowedRisks[item.RiskLevel] || strings.TrimSpace(item.Preconditions.Subject) == "" {
			return fmt.Errorf("case %q has invalid category, risk or fixture", item.ID)
		}
		if !ValidFixtureName(item.Preconditions.Fixture) {
			return fmt.Errorf("case %q has invalid fixture name %q", item.ID, item.Preconditions.Fixture)
		}
		if item.Timeout == "" {
			return fmt.Errorf("case %q timeout is required", item.ID)
		}
		timeout, err := time.ParseDuration(item.Timeout)
		if err != nil || timeout <= 0 {
			return fmt.Errorf("case %q timeout must be positive: %w", item.ID, err)
		}
		if item.RetryBudget < 0 || item.IdempotencyKey == "" || item.CleanupPolicy != "isolated_run" {
			return fmt.Errorf("case %q has invalid retry, idempotency or cleanup policy", item.ID)
		}
		if len(item.RequestSteps) == 0 || !allowedTerminals[item.ExpectedResults.Terminal] {
			return fmt.Errorf("case %q requires request steps and terminal expectation", item.ID)
		}
		if item.ExpectedResults.Terminal != "request_error" && len(item.ExpectedResults.EventTypes) == 0 {
			return fmt.Errorf("case %q requires expected event types", item.ID)
		}
		for _, eventType := range item.ExpectedResults.EventTypes {
			if !allowedEventTypes[eventType] {
				return fmt.Errorf("case %q references invalid event type %q", item.ID, eventType)
			}
		}
		if len(item.EvidenceRequirements) == 0 || len(item.EvaluatorRules) == 0 {
			return fmt.Errorf("case %q requires evidence requirements and evaluator rules", item.ID)
		}
		if item.ForbiddenEffects.MaxWrites < 0 {
			return fmt.Errorf("case %q max_writes cannot be negative", item.ID)
		}
		if err := validatePostconditions(item.ID, item.Postconditions); err != nil {
			return err
		}
		seenEvidence := map[string]bool{}
		for _, requirement := range item.EvidenceRequirements {
			if !allowedEvidence[requirement] || seenEvidence[requirement] {
				return fmt.Errorf("case %q references invalid or duplicate evidence requirement %q", item.ID, requirement)
			}
			seenEvidence[requirement] = true
		}
		for _, requirement := range RequiredEvidence(item) {
			if !seenEvidence[requirement] {
				return fmt.Errorf("case %q must declare required evidence %q", item.ID, requirement)
			}
		}
		seenRules := map[string]bool{}
		for _, step := range item.RequestSteps {
			if !allowedActions[step.Action] {
				return fmt.Errorf("case %q has unsupported action %q", item.ID, step.Action)
			}
			if step.Action == "approval" && step.Decision != "approve" && step.Decision != "reject" {
				return fmt.Errorf("case %q has invalid approval decision", item.ID)
			}
			if step.CancelAfterMS < 0 {
				return fmt.Errorf("case %q cancel_after_ms cannot be negative", item.ID)
			}
			if step.Action == "chat_cancel" && step.CancelAfterMS == 0 {
				return fmt.Errorf("case %q chat_cancel requires a positive cancel_after_ms", item.ID)
			}
			if step.Action != "chat_cancel" && step.CancelAfterMS > 0 {
				return fmt.Errorf("case %q cancel_after_ms is only valid for chat_cancel", item.ID)
			}
		}
		for _, rule := range item.EvaluatorRules {
			if !allowedEvaluators[rule] || seenRules[rule] {
				return fmt.Errorf("case %q references unknown evaluator %q", item.ID, rule)
			}
			seenRules[rule] = true
		}
		for _, rule := range requiredEvaluators {
			if !seenRules[rule] {
				return fmt.Errorf("case %q must declare evaluator %q", item.ID, rule)
			}
		}
	}
	return nil
}

func containsUnsafeText(item Case) bool {
	values := []string{item.ID, item.Version, item.BusinessGoal, item.Preconditions.Fixture, item.Preconditions.Subject, item.Timeout, item.IdempotencyKey, item.CleanupPolicy}
	for _, step := range item.RequestSteps {
		values = append(values, step.Action, step.Message, step.ConversationID, step.Decision, step.RunID, step.Subject)
	}
	values = append(values, item.ImpactTags...)
	values = append(values, item.EvidenceRequirements...)
	values = append(values, item.EvaluatorRules...)
	if database := item.Postconditions.Database; database != nil {
		values = append(values, database.Backend)
		for _, order := range database.ExpectedOrders {
			values = append(values, order.TotalAmount)
		}
	}
	if logs := item.Postconditions.Logs; logs != nil {
		values = append(values, logs.RequiredPhases...)
		values = append(values, logs.RequiredErrorCodes...)
	}
	for _, value := range values {
		lower := strings.ToLower(value)
		for _, marker := range []string{"{{", "}}", "${", "eval(", "exec(", "http://", "https://", "bearer ", "authorization", "password", "api_key", "sk-"} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	}
	return resultMapContainsUnsafe(item.ExpectedResults.Result)
}

func validatePostconditions(caseID string, postconditions Postconditions) error {
	if database := postconditions.Database; database != nil {
		if database.Backend != "mysql" {
			return fmt.Errorf("case %q database backend %q is not registered", caseID, database.Backend)
		}
		if database.BeforeOrderCount == nil && database.AfterOrderCount == nil && len(database.ExpectedOrders) == 0 {
			return fmt.Errorf("case %q database postcondition is empty", caseID)
		}
		for index, order := range database.ExpectedOrders {
			if order.UserID == 0 || order.ProductID == 0 || order.Quantity <= 0 || order.TotalAmount == "" {
				return fmt.Errorf("case %q database expected order %d is incomplete", caseID, index)
			}
		}
		if database.BeforeOrderCount != nil && *database.BeforeOrderCount < 0 || database.AfterOrderCount != nil && *database.AfterOrderCount < 0 {
			return fmt.Errorf("case %q database order counts cannot be negative", caseID)
		}
	}
	if logs := postconditions.Logs; logs != nil {
		if logs.MinRecords < 0 {
			return fmt.Errorf("case %q log min_records cannot be negative", caseID)
		}
		if len(logs.RequiredPhases) == 0 && len(logs.RequiredErrorCodes) == 0 && logs.MinRecords == 0 {
			return fmt.Errorf("case %q log postcondition is empty", caseID)
		}
		if err := validateUniqueNonEmpty("required_phases", logs.RequiredPhases); err != nil {
			return fmt.Errorf("case %q: %w", caseID, err)
		}
		if err := validateUniqueNonEmpty("required_error_codes", logs.RequiredErrorCodes); err != nil {
			return fmt.Errorf("case %q: %w", caseID, err)
		}
	}
	return nil
}

func validateUniqueNonEmpty(name string, values []string) error {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" || seen[value] {
			return fmt.Errorf("%s contains an empty or duplicate value", name)
		}
		seen[value] = true
	}
	return nil
}

func resultMapContainsUnsafe(values map[string]any) bool {
	for key, value := range values {
		if containsUnsafeTextValue(key) || containsUnsafeTextValue(value) {
			return true
		}
	}
	return false
}

func containsUnsafeTextValue(value any) bool {
	switch typed := value.(type) {
	case string:
		lower := strings.ToLower(typed)
		for _, marker := range []string{"{{", "}}", "${", "eval(", "exec(", "http://", "https://", "bearer ", "authorization", "password", "api_key", "sk-"} {
			if strings.Contains(lower, marker) {
				return true
			}
		}
	case map[string]any:
		return resultMapContainsUnsafe(typed)
	case []any:
		for _, item := range typed {
			if containsUnsafeTextValue(item) {
				return true
			}
		}
	}
	return false
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(json.Token) error
	walk = func(token json.Token) error {
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key := keyToken.(string)
				if seen[key] {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				seen[key] = true
				value, err := decoder.Token()
				if err != nil {
					return err
				}
				if err := walk(value); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				value, err := decoder.Token()
				if err != nil {
					return err
				}
				if err := walk(value); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		}
		return nil
	}
	first, err := decoder.Token()
	if err != nil {
		return err
	}
	return walk(first)
}

// ValidFixtureName reports whether a Case may reference this fixture name.
//
// The name identifies which environment the Case expects and is carried into
// the report as correlation metadata. The environment itself is chosen by the
// configuration the Runner starts, so this check is about the reference
// format, not about an allow-list of environments.
func ValidFixtureName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return false
	}
	for index, char := range name {
		switch {
		case char >= 'a' && char <= 'z':
		case (char >= '0' && char <= '9') || char == '_':
			if index == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
