package evaluator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	eval "github.com/observer-mimiron/supervisor-template/eval"
)

// TriageItem turns one failed assertion into an actionable item.
//
// A report answers "what failed"; a triage answers "what do I do next". The
// difference matters: without it the evaluation loop is one-way (run → report)
// and the same defect can escape again, because nothing forces the fix to be
// accompanied by a Case that would have caught it.
type TriageItem struct {
	CaseID string `json:"case_id"`
	// Assertion is the stable identifier of the failed check, e.g. "terminal".
	Assertion string `json:"failed_assertion"`
	// Taxonomy is the failure class, e.g. "business_expectation".
	Taxonomy string `json:"failure_taxonomy"`
	// EvidenceRef points at the evidence a human should read first.
	EvidenceRef string `json:"evidence_reference"`
	// Suspect names where to look first, derived from the taxonomy only.
	Suspect string `json:"suspect"`
	// NextAction is the first concrete step for this class of failure.
	NextAction string `json:"next_action"`
	// RegressionObligation is the loop-closing requirement: a fix is not
	// finished until a Case exists that fails without it.
	RegressionObligation string `json:"regression_obligation"`
}

const regressionObligation = "add or extend a Case that fails without this fix, and prove it with ./eval/mutation-gate.sh (inject the fault, expect the Case to turn red); record the run output with the change"

// suspects maps a failure taxonomy to where to look first. It is deliberately
// small and stable: a triage that guesses widely is worse than no triage.
var suspects = map[string]struct {
	suspect string
	action  string
}{
	"business_expectation": {
		suspect: "用例声明的期望值，或实现返回的业务字段/事件序列",
		action:  "先确认期望值是否仍然是人工评审过的正确行为；确认后再看实现",
	},
	"architecture": {
		suspect: "是否绕过启动期注册表、Worker allow-list 或 Policy Gate",
		action:  "比对事件顺序：Policy/Plan 证据必须出现在 tool_call 之前",
	},
	"implementation": {
		suspect: "副作用路径：审批绑定、幂等键、写入归因",
		action:  "确认批准前无写入、同一幂等键只写一次、只读 Tool 不提供写入归因",
	},
	"environment_dependency": {
		suspect: "超时/重试预算、trace 关联、cleanup 或环境不稳定",
		action:  "先在本地复现一次；只有可复现才是回归，否则记为环境问题",
	},
	"evaluator_rule": {
		suspect: "评测合同本身：用例声明、必需证据或 profile/executor 不匹配",
		action:  "这是评测侧问题，不要改被测实现；修用例数据或先修评测合同",
	},
	"database_state": {
		suspect: "后置条件探针读取的最终状态，或写入没有真正提交",
		action:  "核对数据库前后计数与字段摘要，确认断言针对的是最终状态",
	},
	"diagnostic_logs": {
		suspect: "脱敏后的日志投影，或日志根本没被采集到",
		action:  "确认日志阶段/错误码存在，且没有敏感字段混入",
	},
}

// Triage extracts the failed assertions of a report into actionable items,
// ordered by case then assertion so the output is stable across runs.
func Triage(report Report) []TriageItem {
	items := make([]TriageItem, 0)
	for _, item := range report.Cases {
		if item.Passed {
			continue
		}
		results := make(map[string]Result, len(item.Results))
		for _, result := range item.Results {
			results[result.Evaluator] = result
		}
		for _, failure := range item.Failures {
			taxonomy := failureTaxonomy(item, failure, results)
			entry, ok := suspects[taxonomy]
			if !ok {
				entry = struct {
					suspect string
					action  string
				}{suspect: "未分类失败", action: "人工判断失败类别后补充映射"}
			}
			items = append(items, TriageItem{
				CaseID:               failure.CaseID,
				Assertion:            failure.FailedAssertion,
				Taxonomy:             taxonomy,
				EvidenceRef:          failure.EvidenceReference,
				Suspect:              entry.suspect,
				NextAction:           entry.action,
				RegressionObligation: regressionObligation,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CaseID != items[j].CaseID {
			return items[i].CaseID < items[j].CaseID
		}
		return items[i].Assertion < items[j].Assertion
	})
	return items
}

func failureTaxonomy(item CaseReport, failure eval.FailureRecord, results map[string]Result) string {
	if result, ok := results[failure.Evaluator]; ok && result.FailureTaxonomy != "" {
		return result.FailureTaxonomy
	}
	for _, result := range item.Results {
		if result.FailedAssertion == failure.FailedAssertion && result.FailureTaxonomy != "" {
			return result.FailureTaxonomy
		}
	}
	return "unclassified"
}

// WriteTriage writes the triage items as JSON.
func WriteTriage(path string, items []TriageItem) error {
	if path == "" {
		return fmt.Errorf("triage path is empty")
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// RenderTriage renders the triage as reviewable markdown for a human reader.
func RenderTriage(items []TriageItem) string {
	var builder strings.Builder
	if len(items) == 0 {
		builder.WriteString("无失败断言：没有任何待办。\n")
		return builder.String()
	}
	builder.WriteString("| Case | 断言 | 失败类别 | 先看哪里 |\n| --- | --- | --- | --- |\n")
	for _, item := range items {
		fmt.Fprintf(&builder, "| %s | %s | %s | %s |\n", item.CaseID, item.Assertion, item.Taxonomy, item.Suspect)
	}
	builder.WriteString("\n第一步：\n\n")
	seen := map[string]bool{}
	for _, item := range items {
		if seen[item.NextAction] {
			continue
		}
		seen[item.NextAction] = true
		fmt.Fprintf(&builder, "- %s：%s\n", item.CaseID, item.NextAction)
	}
	fmt.Fprintf(&builder, "\n收尾要求：%s\n", items[0].RegressionObligation)
	return builder.String()
}
