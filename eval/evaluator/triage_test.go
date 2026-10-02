package evaluator

import (
	"strings"
	"testing"

	eval "github.com/observer-mimiron/supervisor-template/eval"
	"github.com/observer-mimiron/supervisor-template/eval/runner"
)

func failingReport(taxonomy, assertion string) Report {
	result := Result{Evaluator: "business_correctness", Version: Version, Passed: false, FailedAssertion: assertion, FailureTaxonomy: taxonomy, EvidenceRef: "case-1"}
	item := CaseReport{
		CaseID: "case-1", CaseVersion: "1", Passed: false,
		Results:  []Result{result},
		Evidence: runner.Evidence{},
		Failures: []eval.FailureRecord{{CaseID: "case-1", CaseVersion: "1", Evaluator: "business_correctness", EvaluatorVersion: Version, FailedAssertion: assertion, EvidenceReference: "case-1"}},
	}
	return Report{ReportSchemaVersion: ReportSchemaVersion, Cases: []CaseReport{item}}
}

func TestTriageTurnsFailuresIntoActionableItems(t *testing.T) {
	items := Triage(failingReport("business_expectation", "result"))
	if len(items) != 1 {
		t.Fatalf("items=%d, want 1", len(items))
	}
	item := items[0]
	if item.CaseID != "case-1" || item.Assertion != "result" || item.Taxonomy != "business_expectation" {
		t.Fatalf("unexpected triage item: %#v", item)
	}
	if item.Suspect == "" || item.NextAction == "" {
		t.Fatalf("triage item is not actionable: %#v", item)
	}
	// 闭环的关键：修完必须补一个能抓住它的用例，否则同一缺陷会再次逃逸。
	if !strings.Contains(item.RegressionObligation, "mutation-gate") {
		t.Fatalf("triage is missing the regression obligation: %q", item.RegressionObligation)
	}
}

func TestTriageIsEmptyForPassingReport(t *testing.T) {
	report := Report{ReportSchemaVersion: ReportSchemaVersion, Cases: []CaseReport{{CaseID: "case-1", Passed: true}}}
	if items := Triage(report); len(items) != 0 {
		t.Fatalf("passing report produced triage items: %#v", items)
	}
	if rendered := RenderTriage(nil); !strings.Contains(rendered, "无失败断言") {
		t.Fatalf("empty triage rendered as %q", rendered)
	}
}

// 未映射的失败类别不能静默丢失：必须仍然产出条目并提示人工判断。
func TestTriageKeepsUnknownTaxonomy(t *testing.T) {
	items := Triage(failingReport("something_new", "terminal"))
	if len(items) != 1 || items[0].Taxonomy != "something_new" {
		t.Fatalf("unknown taxonomy was dropped: %#v", items)
	}
	if !strings.Contains(items[0].NextAction, "人工判断") {
		t.Fatalf("unknown taxonomy is not flagged for human judgement: %#v", items[0])
	}
}

func TestRenderTriageListsNextActions(t *testing.T) {
	rendered := RenderTriage(Triage(failingReport("implementation", "side_effect_attribution")))
	for _, want := range []string{"| Case |", "case-1", "第一步", "收尾要求"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered triage is missing %q:\n%s", want, rendered)
		}
	}
}
