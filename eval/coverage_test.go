package eval

import (
	"path/filepath"
	"strings"
	"testing"
)

func coverageFor(claims ...Claim) Coverage {
	return Coverage{Dataset: "d@1", Claims: claims}
}

func datasetWithCases(ids ...string) Dataset {
	dataset := Dataset{Name: "d", Version: "1"}
	for _, id := range ids {
		dataset.Cases = append(dataset.Cases, Case{ID: id, Version: "1"})
	}
	return dataset
}

func TestCoverageValidateRules(t *testing.T) {
	for _, test := range []struct {
		name     string
		coverage Coverage
		want     string
	}{
		{name: "no dataset", coverage: Coverage{Claims: []Claim{{ID: "a", Statement: "s", Cases: []string{"c"}}}}, want: "requires dataset"},
		{name: "no claims", coverage: Coverage{Dataset: "d@1"}, want: "requires claims"},
		{name: "missing id", coverage: coverageFor(Claim{Statement: "s", Cases: []string{"c"}}), want: "requires id"},
		{name: "duplicate id", coverage: coverageFor(Claim{ID: "a", Statement: "s", Cases: []string{"c"}}, Claim{ID: "a", Statement: "s", Cases: []string{"c"}}), want: "duplicate claim"},
		{name: "missing statement", coverage: coverageFor(Claim{ID: "a", Cases: []string{"c"}}), want: "requires statement"},
		{name: "waived without reason", coverage: coverageFor(Claim{ID: "a", Statement: "s", Waived: true}), want: "waived without a reason"},
		{name: "waived and mapped", coverage: coverageFor(Claim{ID: "a", Statement: "s", Waived: true, WaiverReason: "r", Cases: []string{"c"}}), want: "both waived and mapped"},
		{name: "neither cases nor waiver", coverage: coverageFor(Claim{ID: "a", Statement: "s"}), want: "neither cases nor a waiver"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.coverage.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

// 一个 claim 指向不存在的用例时必须失败：那是最危险的假覆盖——看起来有映射，实际没验。
func TestCheckCoverageRejectsUnknownCase(t *testing.T) {
	coverage := coverageFor(Claim{ID: "a", Statement: "s", Cases: []string{"ghost"}})
	report, err := CheckCoverage(coverage, datasetWithCases("real"))
	if err == nil || !strings.Contains(err.Error(), "evaluation_setup_error") {
		t.Fatalf("error=%v, want a setup error", err)
	}
	if len(report.OrphanCases) != 1 {
		t.Fatalf("orphans=%v, want the unknown case", report.OrphanCases)
	}
}

func TestCheckCoverageRequiresMatchingDatasetVersion(t *testing.T) {
	coverage := coverageFor(Claim{ID: "a", Statement: "s", Cases: []string{"c"}})
	coverage.Dataset = "d@2"
	if _, err := CheckCoverage(coverage, datasetWithCases("c")); err == nil || !strings.Contains(err.Error(), "does not") && !strings.Contains(err.Error(), "targets") {
		t.Fatalf("error=%v, want a dataset mismatch", err)
	}
}

// 没有 claim 指向的用例只是提示，不是失败：用例可以服务多个 claim，或作为额外回归。
func TestCheckCoverageReportsUnreferencedWithoutFailing(t *testing.T) {
	coverage := coverageFor(Claim{ID: "a", Statement: "s", Cases: []string{"used"}})
	report, err := CheckCoverage(coverage, datasetWithCases("used", "extra"))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Unreferenced) != 1 || report.Unreferenced[0] != "extra" {
		t.Fatalf("unreferenced=%v, want [extra]", report.Unreferenced)
	}
	if report.Covered != 1 || report.Waived != 0 {
		t.Fatalf("summary=%s", report.Summary())
	}
}

// 随仓库发布的对照表必须始终与随仓库发布的数据集一致；这条测试防止两者悄悄漂移。
func TestShippedCoverageMatchesShippedDataset(t *testing.T) {
	coverage, err := LoadCoverage(filepath.Join("coverage.json"))
	if err != nil {
		t.Fatalf("shipped coverage does not load: %v", err)
	}
	dataset, err := Load(filepath.Join("datasets", "synthetic-operations-v5.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := CheckCoverage(coverage, dataset)
	if err != nil {
		t.Fatalf("shipped coverage does not match the shipped dataset: %v", err)
	}
	if report.Covered < 1 || report.Waived < 1 {
		t.Fatalf("shipped coverage looks empty: %s", report.Summary())
	}
}
