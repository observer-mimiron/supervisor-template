package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Coverage is the reviewed mapping from claims (a requirement, a user story or
// a changed behaviour) to the Cases that verify them.
//
// The gate can only be trusted if something checks that a claim has a Case at
// all. Without this file a requirement can be entirely unverified while every
// selected Case passes, because the Case set simply never mentioned it.
type Coverage struct {
	Dataset string   `json:"dataset"`
	Claims  []Claim  `json:"claims"`
	Notes   []string `json:"notes,omitempty"`
}

// Claim is one verifiable statement and how it is verified.
type Claim struct {
	// ID is stable and reviewable, e.g. "FR-007" or "cancel-signal".
	ID string `json:"id"`
	// Source points at where the claim comes from (spec, ADR, issue).
	Source string `json:"source,omitempty"`
	// Statement is the claim in one line.
	Statement string `json:"statement"`
	// Cases lists the Case IDs that verify it. Required unless waived.
	Cases []string `json:"cases,omitempty"`
	// Waived means the claim is deliberately not verified, with a reason. A
	// waiver is a reviewed decision, not an omission.
	Waived bool `json:"waived,omitempty"`
	// WaiverReason is required when Waived is true.
	WaiverReason string `json:"waiver_reason,omitempty"`
}

// CoverageReport is the result of validating a coverage file against a dataset.
type CoverageReport struct {
	Claims       int
	Covered      int
	Waived       int
	OrphanCases  []string
	Unreferenced []string
}

// LoadCoverage reads and validates a coverage file's own consistency.
func LoadCoverage(path string) (Coverage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Coverage{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var coverage Coverage
	if err := decoder.Decode(&coverage); err != nil {
		return Coverage{}, fmt.Errorf("decode coverage: %w", err)
	}
	if err := coverage.Validate(); err != nil {
		return Coverage{}, err
	}
	return coverage, nil
}

// Validate checks the coverage file in isolation: unique IDs, and every claim
// either mapped to Cases or explicitly waived with a reason.
func (c Coverage) Validate() error {
	if strings.TrimSpace(c.Dataset) == "" {
		return errors.New("coverage requires dataset")
	}
	if len(c.Claims) == 0 {
		return errors.New("coverage requires claims")
	}
	seen := map[string]bool{}
	for _, claim := range c.Claims {
		if strings.TrimSpace(claim.ID) == "" {
			return errors.New("coverage claim requires id")
		}
		if seen[claim.ID] {
			return fmt.Errorf("coverage has duplicate claim %q", claim.ID)
		}
		seen[claim.ID] = true
		if strings.TrimSpace(claim.Statement) == "" {
			return fmt.Errorf("coverage claim %q requires statement", claim.ID)
		}
		switch {
		case claim.Waived && strings.TrimSpace(claim.WaiverReason) == "":
			return fmt.Errorf("coverage claim %q is waived without a reason", claim.ID)
		case claim.Waived && len(claim.Cases) > 0:
			return fmt.Errorf("coverage claim %q is both waived and mapped to cases", claim.ID)
		case !claim.Waived && len(claim.Cases) == 0:
			return fmt.Errorf("coverage claim %q has neither cases nor a waiver", claim.ID)
		}
	}
	return nil
}

// CheckCoverage cross-checks the coverage file against a dataset: mapped Cases
// must exist, and it reports Cases no claim refers to.
func CheckCoverage(coverage Coverage, dataset Dataset) (CoverageReport, error) {
	if err := coverage.Validate(); err != nil {
		return CoverageReport{}, err
	}
	if want := dataset.Name + "@" + dataset.Version; coverage.Dataset != want {
		return CoverageReport{}, fmt.Errorf("evaluation_setup_error: coverage targets %s but the dataset is %s", coverage.Dataset, want)
	}
	known := map[string]bool{}
	for _, item := range dataset.Cases {
		known[item.ID] = true
	}
	referenced := map[string]bool{}
	report := CoverageReport{Claims: len(coverage.Claims)}
	for _, claim := range coverage.Claims {
		if claim.Waived {
			report.Waived++
			continue
		}
		report.Covered++
		for _, caseID := range claim.Cases {
			if !known[caseID] {
				report.OrphanCases = append(report.OrphanCases, fmt.Sprintf("%s -> %s", claim.ID, caseID))
				continue
			}
			referenced[caseID] = true
		}
	}
	if len(report.OrphanCases) > 0 {
		sort.Strings(report.OrphanCases)
		return report, fmt.Errorf("evaluation_setup_error: coverage references unknown cases: %s", strings.Join(report.OrphanCases, ", "))
	}
	for _, item := range dataset.Cases {
		if !referenced[item.ID] {
			report.Unreferenced = append(report.Unreferenced, item.ID)
		}
	}
	sort.Strings(report.Unreferenced)
	return report, nil
}

// Summary renders a one-line result for CI output.
func (r CoverageReport) Summary() string {
	return fmt.Sprintf("coverage claims=%d covered=%d waived=%d cases_without_claim=%d",
		r.Claims, r.Covered, r.Waived, len(r.Unreferenced))
}
