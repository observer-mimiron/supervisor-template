package evaluator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	eval "github.com/observer-mimiron/supervisor-template/eval"
	"github.com/observer-mimiron/supervisor-template/eval/runner"
)

const Version = "2"

var defaultRules = []string{"business_correctness@1", "architecture_boundary@1", "side_effect_safety@1", "stability@1"}

type Result struct {
	Evaluator        string                 `json:"evaluator_name"`
	Version          string                 `json:"evaluator_version"`
	Passed           bool                   `json:"passed"`
	Reason           string                 `json:"reason"`
	EvidenceRef      string                 `json:"evidence_reference"`
	FailedAssertion  string                 `json:"failed_assertion,omitempty"`
	FailureTaxonomy  string                 `json:"failure_taxonomy,omitempty"`
	HumanDisposition *eval.HumanDisposition `json:"human_disposition"`
}

type CaseReport struct {
	CaseID      string               `json:"case_id"`
	CaseVersion string               `json:"case_version"`
	RunID       string               `json:"run_id"`
	Passed      bool                 `json:"passed"`
	Results     []Result             `json:"results"`
	Evidence    runner.Evidence      `json:"evidence"`
	Failures    []eval.FailureRecord `json:"failures,omitempty"`
}

type Report struct {
	DatasetName    string       `json:"dataset_name"`
	DatasetVersion string       `json:"dataset_version"`
	CodeVersion    string       `json:"code_version"`
	RiskLevel      string       `json:"risk_level,omitempty"`
	ImpactTags     []string     `json:"impact_tags,omitempty"`
	GeneratedAt    time.Time    `json:"generated_at"`
	Passed         int          `json:"passed"`
	Failed         int          `json:"failed"`
	Cases          []CaseReport `json:"cases"`
}

func Evaluate(item eval.Case, evidence runner.Evidence) CaseReport {
	// The four dimensions are a hard gate. The case declaration is validated
	// as metadata, but it cannot remove a dimension from evaluation.
	results := make([]Result, 0, len(defaultRules)+2)
	for _, rule := range defaultRules {
		switch rule {
		case "business_correctness@1":
			results = append(results, business(item, evidence))
		case "architecture_boundary@1":
			results = append(results, architecture(item, evidence))
		case "side_effect_safety@1":
			results = append(results, sideEffects(item, evidence))
		case "stability@1":
			results = append(results, stability(item, evidence))
		}
	}
	if invalid := invalidEvaluatorDeclarations(item.EvaluatorRules); len(invalid) > 0 {
		results = append(results, withFailure(Result{
			Evaluator: "evaluator_contract", Version: Version,
			Reason: fmt.Sprintf("invalid evaluator declarations: %s", strings.Join(invalid, ",")), EvidenceRef: item.ID,
		}, invalid[0], "evaluator_rule"))
	}
	if invalid := evidenceIntegrity(item, evidence); len(invalid) > 0 {
		results = append(results, withFailure(Result{
			Evaluator: "evidence_integrity", Version: Version,
			Reason: fmt.Sprintf("invalid evidence: %s", strings.Join(invalid, ",")), EvidenceRef: item.ID,
		}, invalid[0], "evaluator_rule"))
	}
	if missing := missingEvidence(item, evidence); len(missing) > 0 {
		results = append(results, withFailure(Result{
			Evaluator: "evidence_contract", Version: Version, Passed: false,
			Reason: fmt.Sprintf("missing required evidence: %s", strings.Join(missing, ",")), EvidenceRef: item.ID,
		}, "missing_evidence", "evaluator_rule"))
	}
	passed := true
	for _, result := range results {
		passed = passed && result.Passed
	}
	failures := make([]eval.FailureRecord, 0)
	for _, result := range results {
		if result.Passed {
			continue
		}
		failures = append(failures, eval.FailureRecord{
			CaseID: item.ID, CaseVersion: item.Version, CodeVersion: evidence.CodeVersion,
			RunID: evidence.RunID, Evaluator: result.Evaluator, EvaluatorVersion: result.Version,
			FailedAssertion: result.FailedAssertion, EvidenceReference: result.EvidenceRef,
			HumanDisposition: result.HumanDisposition,
		})
	}
	return CaseReport{CaseID: item.ID, CaseVersion: item.Version, RunID: evidence.RunID, Passed: passed, Results: results, Evidence: evidence, Failures: failures}
}

func Build(dataset eval.Dataset, codeVersion string, cases []CaseReport) Report {
	return BuildWithSelection(dataset, codeVersion, eval.Selection{}, cases)
}

func BuildWithSelection(dataset eval.Dataset, codeVersion string, selection eval.Selection, cases []CaseReport) Report {
	selected, selectionErr := eval.SelectCases(dataset, selection)
	if selectionErr != nil {
		// Invalid selection must not turn into an empty, passing report. Fall
		// back to the full dataset and attach a hard contract failure below.
		selected = append([]eval.Case(nil), dataset.Cases...)
	}
	expected := make(map[string]eval.Case, len(selected))
	for _, item := range selected {
		expected[item.ID+"@"+item.Version] = item
	}
	verified := make([]CaseReport, 0, len(selected)+len(cases))
	seen := make(map[string]bool, len(cases))
	for _, candidate := range cases {
		key := candidate.CaseID + "@" + candidate.CaseVersion
		item, ok := expected[key]
		if !ok {
			verified = append(verified, failedContractCase(candidate.CaseID, candidate.CaseVersion, codeVersion, "case report is not part of selected dataset"))
			continue
		}
		if seen[key] {
			verified = append(verified, failedContractCase(candidate.CaseID, candidate.CaseVersion, codeVersion, "duplicate case report"))
			continue
		}
		seen[key] = true
		// Re-evaluate from immutable Case + Evidence; never trust a caller's
		// Passed/Results fields when constructing the gate report.
		report := Evaluate(item, candidate.Evidence)
		if selectionErr != nil {
			report = addContractFailure(report, codeVersion, "invalid case selection: "+selectionErr.Error())
		}
		verified = append(verified, report)
	}
	for key, item := range expected {
		if !seen[key] {
			verified = append(verified, failedContractCase(item.ID, item.Version, codeVersion, "missing case report"))
		}
	}
	report := Report{DatasetName: dataset.Name, DatasetVersion: dataset.Version, CodeVersion: codeVersion, RiskLevel: selection.RiskLevel, ImpactTags: append([]string(nil), selection.ImpactTags...), GeneratedAt: time.Now().UTC(), Cases: verified}
	for _, item := range verified {
		if item.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	return report
}

func addContractFailure(report CaseReport, codeVersion, reason string) CaseReport {
	result := withFailure(Result{
		Evaluator: "evaluator_contract", Version: Version, Reason: reason,
		EvidenceRef: report.CaseID + "@" + report.CaseVersion,
	}, "selection", "evaluator_rule")
	report.Passed = false
	report.Results = append(report.Results, result)
	report.Failures = append(report.Failures, eval.FailureRecord{
		CaseID: report.CaseID, CaseVersion: report.CaseVersion, CodeVersion: codeVersion,
		RunID: report.RunID, Evaluator: result.Evaluator, EvaluatorVersion: result.Version,
		FailedAssertion: result.FailedAssertion, EvidenceReference: result.EvidenceRef,
	})
	return report
}

func invalidEvaluatorDeclarations(declarations []string) []string {
	seen := make(map[string]bool, len(declarations))
	invalid := make([]string, 0)
	for _, declaration := range declarations {
		if !containsString(defaultRules, declaration) {
			invalid = append(invalid, "unknown:"+declaration)
			continue
		}
		if seen[declaration] {
			invalid = append(invalid, "duplicate:"+declaration)
			continue
		}
		seen[declaration] = true
	}
	for _, rule := range defaultRules {
		if !seen[rule] {
			invalid = append(invalid, "missing:"+rule)
		}
	}
	return invalid
}

func failedContractCase(caseID, caseVersion, codeVersion, reason string) CaseReport {
	result := withFailure(Result{
		Evaluator: "evaluator_contract", Version: Version, Reason: reason,
		EvidenceRef: caseID + "@" + caseVersion,
	}, "case_report", "evaluator_rule")
	return CaseReport{
		CaseID: caseID, CaseVersion: caseVersion, Passed: false,
		Results: []Result{result},
		Failures: []eval.FailureRecord{{
			CaseID: caseID, CaseVersion: caseVersion, CodeVersion: codeVersion,
			Evaluator: result.Evaluator, EvaluatorVersion: result.Version,
			FailedAssertion: result.FailedAssertion, EvidenceReference: result.EvidenceRef,
		}},
	}
}

func WriteJSON(path string, report Report) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'))
}

// WriteJSONL emits one report envelope followed by one independently replayable Case line.
func WriteJSONL(path string, report Report) error {
	if path == "" {
		return fmt.Errorf("report path is empty")
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	if err := encoder.Encode(struct {
		DatasetName    string    `json:"dataset_name"`
		DatasetVersion string    `json:"dataset_version"`
		CodeVersion    string    `json:"code_version"`
		RiskLevel      string    `json:"risk_level,omitempty"`
		ImpactTags     []string  `json:"impact_tags,omitempty"`
		GeneratedAt    time.Time `json:"generated_at"`
		Passed         int       `json:"passed"`
		Failed         int       `json:"failed"`
	}{report.DatasetName, report.DatasetVersion, report.CodeVersion, report.RiskLevel, report.ImpactTags, report.GeneratedAt, report.Passed, report.Failed}); err != nil {
		return err
	}
	for _, item := range report.Cases {
		if err := encoder.Encode(item); err != nil {
			return err
		}
	}
	return writeFile(path, output.Bytes())
}

func business(item eval.Case, evidence runner.Evidence) Result {
	want := item.ExpectedResults.EventTypes
	got := make([]string, 0, len(evidence.Events))
	terminalEvents := 0
	for _, event := range evidence.Events {
		got = append(got, event.Type)
		switch event.Type {
		case "completed", "failed", "canceled":
			terminalEvents++
		}
	}
	requestErrorExpected := item.ExpectedResults.Terminal == "request_error"
	terminalOK := evidence.Terminal == item.ExpectedResults.Terminal
	if requestErrorExpected {
		terminalOK = evidence.Terminal == "" && evidence.Error != "" && hasClientError(evidence.HTTPStatus)
	}
	eventsOK := sameStrings(want, got)
	resultOK := expectedResultMatches(item.ExpectedResults.Result, evidence.Result)
	passed := terminalOK && eventsOK && resultOK && terminalEvents <= 1
	reason := fmt.Sprintf("terminal=%s events=%d", evidence.Terminal, len(got))
	failedAssertion := ""
	if !passed {
		reason = fmt.Sprintf("expected terminal=%s/events=%v/result=%v, got terminal=%s/events=%v/result=%v", item.ExpectedResults.Terminal, want, item.ExpectedResults.Result, evidence.Terminal, got, evidence.Result)
		if terminalEvents > 1 {
			failedAssertion = "terminal_event_count"
		} else if !terminalOK {
			failedAssertion = "terminal"
		} else if !eventsOK {
			failedAssertion = "event_types"
		} else {
			failedAssertion = "result"
		}
	}
	return withFailure(Result{Evaluator: "business_correctness", Version: Version, Passed: passed, Reason: reason, EvidenceRef: evidence.CaseID}, failedAssertion, "business_expectation")
}

func architecture(item eval.Case, evidence runner.Evidence) Result {
	registered := make(map[string]bool, len(evidence.RegisteredTools))
	for _, toolID := range evidence.RegisteredTools {
		registered[toolID] = true
	}
	policy := map[string]int64{}
	for eventIndex, event := range evidence.Events {
		sequence := event.Sequence
		if sequence == 0 {
			sequence = int64(eventIndex + 1)
		}
		switch event.Type {
		case "decision":
			key := event.Data["worker_id"] + "/" + event.Data["tool_id"]
			if event.Data["worker_id"] != "" && event.Data["tool_id"] != "" && (policy[key] == 0 || sequence < policy[key]) {
				policy[key] = sequence
			}
		case "plan":
			for index := 1; ; index++ {
				workerID := event.Data[fmt.Sprintf("step_%d_worker_id", index)]
				toolID := event.Data[fmt.Sprintf("step_%d_tool_id", index)]
				if workerID == "" || toolID == "" {
					break
				}
				key := workerID + "/" + toolID
				if policy[key] == 0 || sequence < policy[key] {
					policy[key] = sequence
				}
			}
		}
	}
	passed := true
	failedAssertion := ""
	for eventIndex, event := range evidence.Events {
		if event.Type != "tool_call" {
			continue
		}
		toolID, workerID := event.Data["tool_id"], event.Data["worker_id"]
		if !registered[toolID] {
			passed, failedAssertion = false, "registered_tool"
			break
		}
		allowed := false
		for _, allowedTool := range evidence.WorkerToolAllowList[workerID] {
			if allowedTool == toolID {
				allowed = true
				break
			}
		}
		if !allowed {
			passed, failedAssertion = false, "worker_tool_allow_list"
			break
		}
		policySequence := policy[workerID+"/"+toolID]
		if policySequence == 0 {
			passed, failedAssertion = false, "policy_evidence"
			break
		}
		toolSequence := event.Sequence
		if toolSequence == 0 {
			toolSequence = int64(eventIndex + 1)
		}
		if policySequence >= toolSequence {
			passed, failedAssertion = false, "policy_order"
			break
		}
	}
	reason := "tool calls matched startup registration and policy evidence"
	if !passed {
		reason = "tool call exceeded startup registration or policy evidence"
	}
	return withFailure(Result{Evaluator: "architecture_boundary", Version: Version, Passed: passed, Reason: reason, EvidenceRef: item.ID}, failedAssertion, "architecture")
}

func sideEffects(item eval.Case, evidence runner.Evidence) Result {
	seenTools := map[string]bool{}
	approvedBindings := map[string]bool{}
	seenSideEffects := map[string]bool{}
	passed := evidence.CleanupResult == "ok"
	failedAssertion := ""
	if !passed {
		failedAssertion = "cleanup"
	}
	toolCallCount := 0
	sideEffectToolCalls := 0
	for _, event := range evidence.Events {
		switch event.Type {
		case "approval_required":
			if binding := approvalBindingKey(event.Data); binding != "" {
				approvedBindings[binding] = true
			}
		case "tool_call":
			toolCallCount++
			toolID := event.Data["tool_id"]
			for _, forbidden := range item.ForbiddenEffects.ToolIDs {
				if toolID == forbidden {
					passed, failedAssertion = false, "forbidden_tool"
				}
			}
			metadata, hasMetadata := evidence.RegisteredToolMetadata[toolID]
			if len(evidence.RegisteredTools) > 0 && !hasMetadata {
				passed, failedAssertion = false, "tool_contract"
			}
			if hasMetadata && metadata.Risk == "side_effect" {
				sideEffectToolCalls++
				if binding := approvalBindingKey(event.Data); binding != "" {
					if seenSideEffects[binding] {
						passed, failedAssertion = false, "duplicate_side_effect_tool_call"
					}
					seenSideEffects[binding] = true
				}
			}
			if hasMetadata && (metadata.Risk == "side_effect" || metadata.RequiresApproval) {
				binding := approvalBindingKey(event.Data)
				approved := binding != "" && approvedBindings[binding]
				if !approved {
					passed, failedAssertion = false, "approval_before_side_effect"
				}
			}
			seenTools[toolID] = true
		}
	}
	if toolCallCount == 0 && evidence.FakeWriteCount > 0 {
		passed = false
		if failedAssertion == "" {
			failedAssertion = "write_without_tool_call"
		}
	} else if toolCallCount > 0 && sideEffectToolCalls == 0 && evidence.FakeWriteCount > 0 {
		passed = false
		if failedAssertion == "" {
			failedAssertion = "write_without_side_effect_tool"
		}
	}
	if item.ForbiddenEffects.MaxWrites == 0 && evidence.FakeWriteCount != 0 {
		passed, failedAssertion = false, "max_writes"
	}
	if item.ForbiddenEffects.MaxWrites > 0 && evidence.FakeWriteCount > item.ForbiddenEffects.MaxWrites {
		passed, failedAssertion = false, "max_writes"
	}
	return withFailure(Result{Evaluator: "side_effect_safety", Version: Version, Passed: passed, Reason: fmt.Sprintf("unique tool calls=%d writes=%d", len(seenTools), evidence.FakeWriteCount), EvidenceRef: item.ID}, failedAssertion, "implementation")
}

func approvalBindingKey(data map[string]string) string {
	stepID := strings.TrimSpace(data["step_id"])
	workerID := strings.TrimSpace(data["worker_id"])
	toolID := strings.TrimSpace(data["tool_id"])
	if stepID == "" || workerID == "" || toolID == "" {
		return ""
	}
	return stepID + "/" + workerID + "/" + toolID
}

func stability(item eval.Case, evidence runner.Evidence) Result {
	requestErrorExpected := item.ExpectedResults.Terminal == "request_error" && hasClientError(evidence.HTTPStatus)
	passed := (evidence.Error == "" || requestErrorExpected) && (evidence.Terminal != "" || requestErrorExpected) && evidence.TraceID != ""
	failedAssertion := ""
	if evidence.Error != "" && !requestErrorExpected {
		failedAssertion = "error"
	}
	if failedAssertion == "" && evidence.Terminal == "" && !requestErrorExpected {
		failedAssertion = "terminal"
	}
	if failedAssertion == "" && evidence.TraceID == "" {
		failedAssertion = "trace_id"
	}
	if len(evidence.HTTPStatus) == 0 {
		passed, failedAssertion = false, "http_statuses"
	}
	for _, status := range evidence.HTTPStatus {
		if (status < 200 || status >= 300) && !(requestErrorExpected && status >= 400 && status < 500) {
			passed, failedAssertion = false, "http_statuses"
		}
	}
	if evidence.CleanupResult != "ok" {
		passed, failedAssertion = false, "cleanup"
	}
	if evidence.Retries > item.RetryBudget {
		passed, failedAssertion = false, "retry_budget"
	}
	if evidence.RepeatChecked && !evidence.RepeatVerdictStable {
		passed, failedAssertion = false, "repeat_verdict"
	}
	return withFailure(Result{Evaluator: "stability", Version: Version, Passed: passed, Reason: strings.TrimSpace(fmt.Sprintf("terminal=%s trace=%t elapsed_ms=%d retries=%d/%d", evidence.Terminal, evidence.TraceID != "", evidence.ElapsedMS, evidence.Retries, item.RetryBudget)), EvidenceRef: evidenceReference(item, evidence)}, failedAssertion, "environment_dependency")
}

func missingEvidence(item eval.Case, evidence runner.Evidence) []string {
	missing := make([]string, 0)
	seen := make(map[string]bool)
	requirements := append(append([]string(nil), item.EvidenceRequirements...), eval.RequiredEvidence(item)...)
	for _, requirement := range requirements {
		if seen[requirement] {
			continue
		}
		seen[requirement] = true
		if evidenceSatisfied(item, evidence, requirement) {
			continue
		}
		missing = append(missing, requirement)
	}
	return missing
}

func evidenceIntegrity(item eval.Case, evidence runner.Evidence) []string {
	invalid := make([]string, 0)
	add := func(assertion string) {
		for _, existing := range invalid {
			if existing == assertion {
				return
			}
		}
		invalid = append(invalid, assertion)
	}
	if strings.TrimSpace(evidence.CaseID) == "" || evidence.CaseID != item.ID {
		add("case_id")
	}
	if strings.TrimSpace(evidence.CaseVersion) == "" || evidence.CaseVersion != item.Version {
		add("case_version")
	}
	if strings.TrimSpace(evidence.RunID) == "" {
		add("run_id")
	}
	if strings.TrimSpace(evidence.TraceID) == "" {
		add("trace_correlation")
	}
	seenIDs := make(map[string]bool)
	terminal := ""
	terminalCount := 0
	firstTrace := ""
	for index, event := range evidence.Events {
		if event.Sequence != int64(index+1) {
			add("sequence")
		}
		if strings.TrimSpace(event.EventID) == "" {
			add("event_id")
		}
		if event.EventID != "" {
			if seenIDs[event.EventID] {
				add("event_id")
			}
			seenIDs[event.EventID] = true
		}
		if strings.TrimSpace(event.RunID) == "" || event.RunID != evidence.RunID {
			add("event_run_id")
		}
		if strings.TrimSpace(event.TraceID) == "" {
			add("trace_correlation")
		}
		if event.TraceID != "" {
			if firstTrace == "" {
				firstTrace = event.TraceID
			}
			if evidence.TraceID != "" && event.Sequence == 1 && event.TraceID != evidence.TraceID {
				add("trace_correlation")
			}
		}
		switch event.Type {
		case "completed", "failed", "canceled":
			terminalCount++
			terminal = event.Type
		}
	}
	if firstTrace != "" && evidence.TraceID != firstTrace {
		add("trace_correlation")
	}
	if terminalCount > 1 {
		add("terminal_event_count")
	}
	if evidence.Terminal != "" && (terminalCount != 1 || terminal != evidence.Terminal) {
		add("terminal")
	}
	if evidence.Terminal == "" && terminalCount > 0 {
		add("terminal")
	}
	for toolID, metadata := range evidence.RegisteredToolMetadata {
		if metadata.Risk != "read_only" && metadata.Risk != "side_effect" {
			add("tool_metadata")
		}
		if metadata.Risk == "side_effect" && (!metadata.RequiresApproval || !metadata.IdempotencyRequired) {
			add("tool_metadata")
		}
		if len(evidence.RegisteredTools) > 0 && !containsString(evidence.RegisteredTools, toolID) {
			add("tool_metadata")
		}
	}
	return invalid
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func evidenceSatisfied(item eval.Case, evidence runner.Evidence, requirement string) bool {
	switch requirement {
	case "events":
		return len(evidence.Events) > 0
	case "tool_calls":
		for _, event := range evidence.Events {
			if event.Type == "tool_call" {
				return true
			}
		}
		return false
	case "run_state":
		return evidence.Terminal != "" || item.ExpectedResults.Terminal == "request_error"
	case "trace_correlation":
		return strings.TrimSpace(evidence.TraceID) != ""
	case "approval":
		for _, event := range evidence.Events {
			if event.Type == "approval_required" {
				return true
			}
		}
		return false
	case "cleanup":
		return evidence.CleanupResult == "ok"
	case "http_statuses":
		return len(evidence.HTTPStatus) > 0
	case "sequence":
		for index, event := range evidence.Events {
			if event.Sequence != int64(index+1) {
				return false
			}
		}
		return len(evidence.Events) > 0
	default:
		return false
	}
}

func expectedResultMatches(expected, actual map[string]any) bool {
	for key, want := range expected {
		got, ok := actual[key]
		if !ok || !jsonValuesEqual(want, got) {
			return false
		}
	}
	return true
}

func jsonValuesEqual(left, right any) bool {
	a, err := json.Marshal(left)
	if err != nil {
		return false
	}
	b, err := json.Marshal(right)
	return err == nil && bytes.Equal(a, b)
}

func hasClientError(statuses []int) bool {
	for _, status := range statuses {
		if status >= 400 && status < 500 {
			return true
		}
	}
	return false
}

func evidenceReference(item eval.Case, evidence runner.Evidence) string {
	if evidence.FailedStepID == "" {
		return item.ID
	}
	return item.ID + "#" + evidence.FailedStepID
}

func withFailure(result Result, assertion, taxonomy string) Result {
	if assertion != "" {
		result.FailedAssertion = assertion
		result.FailureTaxonomy = taxonomy
	}
	return result
}

func sameStrings(want, got []string) bool {
	if len(want) != len(got) {
		return false
	}
	for index := range want {
		if want[index] != got[index] {
			return false
		}
	}
	return true
}

func writeFile(path string, data []byte) error {
	if path == "" {
		return fmt.Errorf("report path is empty")
	}
	return os.WriteFile(path, data, 0o600)
}
