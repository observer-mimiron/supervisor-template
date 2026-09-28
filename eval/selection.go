package eval

import (
	"fmt"
	"sort"
	"strings"
)

// Selection is the explicit PR risk declaration consumed by the local gate.
type Selection struct {
	RiskLevel  string
	ImpactTags []string
}

// NormalizeImpactTags parses the comma-separated CLI/CI representation.
func NormalizeImpactTags(value string) []string {
	seen := map[string]bool{}
	var tags []string
	for _, raw := range strings.Split(value, ",") {
		tag := strings.TrimSpace(raw)
		if tag != "" && !seen[tag] {
			seen[tag] = true
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

// SelectCases keeps the low/medium base set and any explicitly tagged cases.
// High or unknown risk is fail-safe and runs the complete dataset.
func SelectCases(dataset Dataset, selection Selection) ([]Case, error) {
	risk := strings.ToLower(strings.TrimSpace(selection.RiskLevel))
	if risk == "" || risk == "high" || (risk != "low" && risk != "medium") {
		return append([]Case(nil), dataset.Cases...), nil
	}
	maxRank := 1
	if risk == "medium" {
		maxRank = 2
	}
	ranks := map[string]int{"low": 1, "medium": 2, "high": 3}
	tagSet := make(map[string]bool, len(selection.ImpactTags))
	for _, tag := range selection.ImpactTags {
		if strings.TrimSpace(tag) == "" {
			return nil, fmt.Errorf("impact tag cannot be empty")
		}
		tagSet[tag] = true
	}
	selected := make([]Case, 0, len(dataset.Cases))
	for _, item := range dataset.Cases {
		include := ranks[item.RiskLevel] <= maxRank
		if !include {
			for _, tag := range item.ImpactTags {
				if tagSet[tag] {
					include = true
					break
				}
			}
		}
		if include {
			selected = append(selected, item)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("risk selection produced no cases")
	}
	return selected, nil
}
