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

const Version = "1"

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
	results := []Result{
		business(item, evidence),
		architecture(item, evidence),
		sideEffects(item, evidence),
		stability(item, evidence),
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
	report := Report{DatasetName: dataset.Name, DatasetVersion: dataset.Version, CodeVersion: codeVersion, RiskLevel: selection.RiskLevel, ImpactTags: append([]string(nil), selection.ImpactTags...), GeneratedAt: time.Now().UTC(), Cases: cases}
	for _, item := range cases {
		if item.Passed {
			report.Passed++
		} else {
			report.Failed++
		}
	}
	return report
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
	for _, event := range evidence.Events {
		got = append(got, event.Type)
	}
	requestErrorExpected := item.ExpectedResults.Terminal == "request_error"
	terminalOK := evidence.Terminal == item.ExpectedResults.Terminal
	if requestErrorExpected {
		terminalOK = evidence.Terminal == "" && evidence.Error != "" && hasClientError(evidence.HTTPStatus)
	}
	eventsOK := sameStrings(want, got)
	resultOK := expectedResultMatches(item.ExpectedResults.Result, evidence.Result)
	passed := terminalOK && eventsOK && resultOK
	reason := fmt.Sprintf("terminal=%s events=%d", evidence.Terminal, len(got))
	failedAssertion := ""
	if !passed {
		reason = fmt.Sprintf("expected terminal=%s/events=%v/result=%v, got terminal=%s/events=%v/result=%v", item.ExpectedResults.Terminal, want, item.ExpectedResults.Result, evidence.Terminal, got, evidence.Result)
		if !terminalOK {
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
	policy := map[string]bool{}
	for _, event := range evidence.Events {
		switch event.Type {
		case "decision":
			policy[event.Data["worker_id"]+"/"+event.Data["tool_id"]] = true
		case "plan":
			for index := 1; ; index++ {
				workerID := event.Data[fmt.Sprintf("step_%d_worker_id", index)]
				toolID := event.Data[fmt.Sprintf("step_%d_tool_id", index)]
				if workerID == "" || toolID == "" {
					break
				}
				policy[workerID+"/"+toolID] = true
			}
		}
	}
	passed := true
	failedAssertion := ""
	for _, event := range evidence.Events {
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
		if !policy[workerID+"/"+toolID] {
			passed, failedAssertion = false, "policy_evidence"
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
	approvalSeen := false
	passed := evidence.CleanupResult == "ok"
	failedAssertion := ""
	if !passed {
		failedAssertion = "cleanup"
	}
	for _, event := range evidence.Events {
		switch event.Type {
		case "approval_required":
			approvalSeen = true
		case "tool_call":
			toolID := event.Data["tool_id"]
			for _, forbidden := range item.ForbiddenEffects.ToolIDs {
				if toolID == forbidden {
					passed, failedAssertion = false, "forbidden_tool"
				}
			}
			if toolID == "simulated_outreach" && !approvalSeen {
				passed, failedAssertion = false, "approval_before_side_effect"
			}
			seenTools[toolID] = true
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
