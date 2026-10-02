package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func baselineFor(cases ...BaselineCase) Baseline {
	return Baseline{
		BaselineName: "synthetic-operations", DatasetName: "synthetic-operations", DatasetVersion: "1",
		EvaluatorVersion: "2", ReportSchemaVersion: 2, ApprovedAt: time.Unix(0, 0).UTC(), Cases: cases,
	}
}

func requestFor(cases ...CurrentVerdict) ComparisonRequest {
	return ComparisonRequest{
		DatasetName: "synthetic-operations", DatasetVersion: "1",
		EvaluatorVersion: "2", ReportSchemaVersion: 2, Cases: cases,
	}
}

// 只有"基线通过、本次失败"和"基线用例消失"才算退化；
// 基线里本来就失败的用例不能反复报成新问题。
func TestBaselineCompareClassifiesOutcomes(t *testing.T) {
	baseline := baselineFor(
		BaselineCase{CaseID: "was-passing", CaseVersion: "1", Passed: true},
		BaselineCase{CaseID: "was-failing", Passed: false},
		BaselineCase{CaseID: "still-passing", Passed: true},
		BaselineCase{CaseID: "vanished", Passed: true},
	)
	comparison, err := baseline.Compare(requestFor(
		CurrentVerdict{CaseID: "was-passing", Passed: false},
		CurrentVerdict{CaseID: "was-failing", Passed: false},
		CurrentVerdict{CaseID: "still-passing", Passed: true},
		CurrentVerdict{CaseID: "brand-new", Passed: true},
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(comparison.Regressions) != 1 || comparison.Regressions[0] != "was-passing" {
		t.Fatalf("regressions=%v, want [was-passing]", comparison.Regressions)
	}
	if len(comparison.Missing) != 1 || comparison.Missing[0] != "vanished" {
		t.Fatalf("missing=%v, want [vanished]", comparison.Missing)
	}
	if len(comparison.New) != 1 || comparison.New[0] != "brand-new" {
		t.Fatalf("new=%v, want [brand-new]", comparison.New)
	}
	if len(comparison.StillFailing) != 1 || comparison.StillFailing[0] != "was-failing" {
		t.Fatalf("still_failing=%v, want [was-failing]", comparison.StillFailing)
	}
	if !comparison.Failed() {
		t.Fatal("a regression plus a missing case must fail the gate")
	}
}

// 基线里已经失败的用例不算新增退化：只报"变更引起的退化"才有意义。
func TestBaselineStillFailingDoesNotFailTheGate(t *testing.T) {
	baseline := baselineFor(BaselineCase{CaseID: "already-broken", Passed: false})
	comparison, err := baseline.Compare(requestFor(CurrentVerdict{CaseID: "already-broken", Passed: false}))
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Failed() {
		t.Fatalf("still-failing case failed the gate: %s", comparison.Summary())
	}
	if len(comparison.StillFailing) != 1 {
		t.Fatalf("still_failing=%v", comparison.StillFailing)
	}
}

// 用例集变化必须被区分出来：新增只是提示，消失必须报错。
func TestBaselineMissingCaseFailsEvenWhenEverythingElsePasses(t *testing.T) {
	baseline := baselineFor(
		BaselineCase{CaseID: "a", Passed: true},
		BaselineCase{CaseID: "b", Passed: true},
	)
	comparison, err := baseline.Compare(requestFor(CurrentVerdict{CaseID: "a", Passed: true}))
	if err != nil {
		t.Fatal(err)
	}
	if !comparison.Failed() || len(comparison.Missing) != 1 {
		t.Fatalf("missing case did not fail the gate: %s", comparison.Summary())
	}
}

// 跨版本比较不是回归信号，是两次不同的测量：必须拒绝而不是静默比较。
func TestBaselineRefusesIncomparableRuns(t *testing.T) {
	baseline := baselineFor(BaselineCase{CaseID: "a", Passed: true})
	for _, test := range []struct {
		name    string
		request ComparisonRequest
		want    string
	}{
		{name: "dataset version", request: ComparisonRequest{DatasetName: "synthetic-operations", DatasetVersion: "2", EvaluatorVersion: "2", ReportSchemaVersion: 2}, want: "does not match baseline"},
		{name: "dataset name", request: ComparisonRequest{DatasetName: "other", DatasetVersion: "1", EvaluatorVersion: "2", ReportSchemaVersion: 2}, want: "does not match baseline"},
		{name: "evaluator version", request: ComparisonRequest{DatasetName: "synthetic-operations", DatasetVersion: "1", EvaluatorVersion: "3", ReportSchemaVersion: 2}, want: "evaluator version"},
		{name: "report schema newer", request: ComparisonRequest{DatasetName: "synthetic-operations", DatasetVersion: "1", EvaluatorVersion: "2", ReportSchemaVersion: 3}, want: "report_schema_version"},
		{name: "report schema older", request: ComparisonRequest{DatasetName: "synthetic-operations", DatasetVersion: "1", EvaluatorVersion: "2", ReportSchemaVersion: 1}, want: "older than the supported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := baseline.Compare(test.request); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestBaselineValidateRejectsIncompleteSnapshots(t *testing.T) {
	for _, test := range []struct {
		name     string
		baseline Baseline
		want     string
	}{
		{name: "no dataset", baseline: Baseline{DatasetVersion: "1", EvaluatorVersion: "2", ReportSchemaVersion: 2, Cases: []BaselineCase{{CaseID: "a"}}}, want: "dataset_name"},
		{name: "no evaluator", baseline: Baseline{DatasetName: "d", DatasetVersion: "1", ReportSchemaVersion: 2, Cases: []BaselineCase{{CaseID: "a"}}}, want: "evaluator_version"},
		{name: "no schema version", baseline: Baseline{DatasetName: "d", DatasetVersion: "1", EvaluatorVersion: "2", Cases: []BaselineCase{{CaseID: "a"}}}, want: "report_schema_version"},
		{name: "no cases", baseline: Baseline{DatasetName: "d", DatasetVersion: "1", EvaluatorVersion: "2", ReportSchemaVersion: 2}, want: "requires cases"},
		{name: "duplicate case", baseline: Baseline{DatasetName: "d", DatasetVersion: "1", EvaluatorVersion: "2", ReportSchemaVersion: 2, Cases: []BaselineCase{{CaseID: "a"}, {CaseID: "a"}}}, want: "duplicate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.baseline.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

// 快照必须能往返读写，并且拒绝未知字段——基线是评审过的数据，多出的字段意味着格式漂移。
func TestBaselineSnapshotRoundTripAndUnknownFields(t *testing.T) {
	built := NewBaseline("synthetic-operations", "synthetic-operations", "1", "2", 2, time.Unix(1_700_000_000, 0), []CurrentVerdict{
		{CaseID: "b", CaseVersion: "1", Passed: true},
		{CaseID: "a", CaseVersion: "1", Passed: false},
	})
	if built.Cases[0].CaseID != "a" {
		t.Fatalf("baseline cases are not sorted: %#v", built.Cases)
	}
	path := filepath.Join(t.TempDir(), "baseline.json")
	writeBaseline(t, path, built)
	loaded, err := LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Cases) != 2 || loaded.Cases[1].CaseID != "b" || loaded.DatasetVersion != "1" {
		t.Fatalf("round trip lost data: %#v", loaded)
	}
	if _, err := loaded.Compare(requestFor(CurrentVerdict{CaseID: "a", Passed: true}, CurrentVerdict{CaseID: "b", Passed: true})); err != nil {
		t.Fatalf("loaded baseline is not comparable: %v", err)
	}

	writeBaseline(t, path, built)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	withUnknown := strings.Replace(string(data), `"baseline_name"`, `"unexpected_field":"x","baseline_name"`, 1)
	if err := os.WriteFile(path, []byte(withUnknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(path); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown baseline field error=%v, want rejection", err)
	}
}

// 基线快照必须记录逐用例的判定身份：只有 pass/fail 标签无法区分
// "同一次测量重跑"与"换了一次测量但标签相同"。
func TestBaselineSnapshotRecordsVerdictDigest(t *testing.T) {
	built := NewBaseline("synthetic-operations", "synthetic-operations", "1", "2", 2, time.Unix(1_700_000_000, 0), []CurrentVerdict{
		{CaseID: "a", CaseVersion: "1", Passed: true, VerdictDigest: "digest-a"},
		{CaseID: "b", CaseVersion: "1", Passed: false, VerdictDigest: "digest-b"},
	})
	path := filepath.Join(t.TempDir(), "baseline.json")
	writeBaseline(t, path, built)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"verdict_digest": "digest-a"`) {
		t.Fatalf("snapshot does not record the per-case verdict digest:\n%s", data)
	}
	loaded, err := LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Cases[0].VerdictDigest != "digest-a" || loaded.Cases[1].VerdictDigest != "digest-b" {
		t.Fatalf("round trip lost the verdict digests: %#v", loaded.Cases)
	}
}

// 判定身份（digest）只提供审计信息，不参与判定：只有 pass/fail 决定退化，
// 一次"标签相同、证据不同"的改动不能被悄悄变成红灯或绿灯。
func TestBaselineComparisonIgnoresVerdictDigest(t *testing.T) {
	baseline := baselineFor(BaselineCase{CaseID: "same-label", CaseVersion: "1", Passed: true, VerdictDigest: "before"})
	comparison, err := baseline.Compare(requestFor(CurrentVerdict{CaseID: "same-label", CaseVersion: "1", Passed: true, VerdictDigest: "after"}))
	if err != nil {
		t.Fatal(err)
	}
	if comparison.Failed() {
		t.Fatalf("a digest-only change failed the gate: %s", comparison.Summary())
	}
	if len(comparison.Unchanged) != 1 {
		t.Fatalf("unchanged=%v, want the case to stay unchanged", comparison.Unchanged)
	}
}

// 重新批准只在"测量本身变了"时刷新：判定身份完全相同则沿用原批准时间，
// 判定、用例版本或摘要任一变化都算一次新的批准。
func TestBaselineSameVerdictsUsesVerdictIdentity(t *testing.T) {
	approved := baselineFor(
		BaselineCase{CaseID: "a", CaseVersion: "1", Passed: true, VerdictDigest: "d1"},
		BaselineCase{CaseID: "b", CaseVersion: "1", Passed: false, VerdictDigest: "d2"},
	)
	if !approved.SameVerdicts(approved) {
		t.Fatal("a baseline must describe the same measurement as itself")
	}
	for _, test := range []struct {
		name   string
		change func(Baseline) Baseline
	}{
		{name: "digest changed", change: func(b Baseline) Baseline {
			b.Cases[0].VerdictDigest = "other"
			return b
		}},
		{name: "verdict changed", change: func(b Baseline) Baseline {
			b.Cases[0].Passed = false
			return b
		}},
		{name: "case version changed", change: func(b Baseline) Baseline {
			b.Cases[1].CaseVersion = "2"
			return b
		}},
		{name: "evaluator version changed", change: func(b Baseline) Baseline {
			b.EvaluatorVersion = "3"
			return b
		}},
		{name: "report schema changed", change: func(b Baseline) Baseline {
			b.ReportSchemaVersion = 3
			return b
		}},
		{name: "case set changed", change: func(b Baseline) Baseline {
			b.Cases = b.Cases[:1]
			return b
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.change(cloneBaseline(approved)).SameVerdicts(approved) {
				t.Fatal("a changed measurement was treated as a re-approval of the same one")
			}
		})
	}
}

func cloneBaseline(source Baseline) Baseline {
	clone := source
	clone.Cases = append([]BaselineCase(nil), source.Cases...)
	return clone
}

func writeBaseline(t *testing.T, path string, baseline Baseline) {
	t.Helper()
	data, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
