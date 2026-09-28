package eval

import "testing"

func TestSelectCasesUsesBaseAndImpactTags(t *testing.T) {
	dataset := Dataset{Cases: []Case{
		{ID: "low", Version: "1", RiskLevel: "low", ImpactTags: []string{"query"}},
		{ID: "medium", Version: "1", RiskLevel: "medium", ImpactTags: []string{"approval"}},
		{ID: "high", Version: "1", RiskLevel: "high", ImpactTags: []string{"approval"}},
	}}
	selected, err := SelectCases(dataset, Selection{RiskLevel: "low", ImpactTags: []string{"approval"}})
	if err != nil || len(selected) != 3 {
		t.Fatalf("low selection=%v err=%v", selected, err)
	}
	selected, err = SelectCases(dataset, Selection{RiskLevel: "medium"})
	if err != nil || len(selected) != 2 {
		t.Fatalf("medium selection=%v err=%v", selected, err)
	}
}

func TestUnknownRiskRunsFullDataset(t *testing.T) {
	dataset := Dataset{Cases: []Case{{ID: "a", Version: "1"}, {ID: "b", Version: "1"}}}
	selected, err := SelectCases(dataset, Selection{RiskLevel: "unclassified"})
	if err != nil || len(selected) != 2 {
		t.Fatalf("unknown selection=%v err=%v", selected, err)
	}
}
