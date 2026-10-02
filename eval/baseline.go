package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Outcome values produced by a baseline comparison. They are intentionally
// few: the gate only needs to tell "got worse" apart from "was already broken"
// and from "the two runs are not comparable".
const (
	OutcomeUnchanged    = "unchanged"
	OutcomeRegression   = "regression"
	OutcomeStillFailing = "still_failing"
	OutcomeNew          = "new"
	OutcomeMissing      = "missing"
)

// CompatibleReportSchemaVersion is the oldest report schema a baseline
// comparison understands. A report outside this range is refused instead of
// being read under the wrong semantics.
const CompatibleReportSchemaVersion = 2

// BaselineCase is one approved per-Case verdict.
//
// VerdictDigest is the verdict identity the approval was based on. It is
// recorded so a reviewer can tell "the same measurement, re-run" apart from
// "a different measurement that happens to have the same pass/fail label":
// the digest covers the observed events, terminal state and projected result,
// and deliberately excludes the per-request trace ID so it stays stable across
// independent runs. It is informational for the gate — only pass/fail decides
// regressions — but it participates in re-approval identity (SameVerdicts).
type BaselineCase struct {
	CaseID        string `json:"case_id"`
	CaseVersion   string `json:"case_version"`
	Passed        bool   `json:"passed"`
	VerdictDigest string `json:"verdict_digest,omitempty"`
}

// Baseline is an approved, versioned snapshot of per-Case verdicts. It is
// reviewed and versioned with the repository so a regression gate stays
// offline and can be rolled back with the code it describes.
type Baseline struct {
	BaselineName        string         `json:"baseline_name"`
	DatasetName         string         `json:"dataset_name"`
	DatasetVersion      string         `json:"dataset_version"`
	EvaluatorVersion    string         `json:"evaluator_version"`
	ReportSchemaVersion int            `json:"report_schema_version"`
	ApprovedAt          time.Time      `json:"approved_at"`
	Cases               []BaselineCase `json:"cases"`
}

// CurrentVerdict is the minimum a comparison needs from a report, so the
// evaluator package can hand its results over without the baseline logic
// depending on it (which would be an import cycle). VerdictDigest is the
// report's per-Case evidence digest; it is recorded in a snapshot but does not
// take part in the comparison itself.
type CurrentVerdict struct {
	CaseID        string
	CaseVersion   string
	Passed        bool
	VerdictDigest string
}

// ComparisonRequest carries the current run's identity and verdicts.
type ComparisonRequest struct {
	DatasetName         string
	DatasetVersion      string
	EvaluatorVersion    string
	ReportSchemaVersion int
	Cases               []CurrentVerdict
}

// CaseOutcome records how one Case compared to the baseline.
type CaseOutcome struct {
	CaseID  string
	Outcome string
}

// BaselineComparison is the result of comparing a run against a baseline.
type BaselineComparison struct {
	Outcomes     []CaseOutcome
	Regressions  []string
	Missing      []string
	New          []string
	StillFailing []string
	Unchanged    []string
}

// Failed reports whether the comparison must fail the gate. Only cases that
// got worse, or disappeared, fail it: a Case that was already failing in the
// approved baseline is not a new regression.
func (c BaselineComparison) Failed() bool {
	return len(c.Regressions) > 0 || len(c.Missing) > 0
}

// Summary renders a stable one-line summary for CI output.
func (c BaselineComparison) Summary() string {
	return fmt.Sprintf("baseline regressions=%d missing=%d new=%d still_failing=%d unchanged=%d",
		len(c.Regressions), len(c.Missing), len(c.New), len(c.StillFailing), len(c.Unchanged))
}

// LoadBaseline reads and validates a baseline snapshot.
func LoadBaseline(path string) (Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Baseline{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var baseline Baseline
	if err := decoder.Decode(&baseline); err != nil {
		return Baseline{}, fmt.Errorf("decode baseline: %w", err)
	}
	if err := baseline.Validate(); err != nil {
		return Baseline{}, err
	}
	return baseline, nil
}

// Validate reports whether a baseline snapshot is usable.
func (b Baseline) Validate() error {
	if strings.TrimSpace(b.DatasetName) == "" || strings.TrimSpace(b.DatasetVersion) == "" {
		return errors.New("baseline requires dataset_name and dataset_version")
	}
	if strings.TrimSpace(b.EvaluatorVersion) == "" {
		return errors.New("baseline requires evaluator_version")
	}
	if b.ReportSchemaVersion == 0 {
		return errors.New("baseline requires report_schema_version")
	}
	if len(b.Cases) == 0 {
		return errors.New("baseline requires cases")
	}
	seen := map[string]bool{}
	for _, item := range b.Cases {
		if strings.TrimSpace(item.CaseID) == "" {
			return errors.New("baseline case requires case_id")
		}
		if seen[item.CaseID] {
			return fmt.Errorf("baseline has duplicate case %q", item.CaseID)
		}
		seen[item.CaseID] = true
	}
	return nil
}

// CheckCompatible verifies that a run may be compared with this baseline.
// Entrypoints call it before executing any Case so an incomparable run fails as
// a setup error and leaves no report behind, instead of producing evidence that
// can never be compared with anything.
func (b Baseline) CheckCompatible(datasetName, datasetVersion, evaluatorVersion string, reportSchemaVersion int) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if reportSchemaVersion < CompatibleReportSchemaVersion {
		return fmt.Errorf("evaluation_setup_error: report_schema_version %d is older than the supported %d", reportSchemaVersion, CompatibleReportSchemaVersion)
	}
	if reportSchemaVersion != b.ReportSchemaVersion {
		return fmt.Errorf("evaluation_setup_error: report_schema_version %d does not match baseline %d", reportSchemaVersion, b.ReportSchemaVersion)
	}
	if datasetName != b.DatasetName || datasetVersion != b.DatasetVersion {
		return fmt.Errorf("evaluation_setup_error: dataset %s@%s does not match baseline %s@%s",
			datasetName, datasetVersion, b.DatasetName, b.DatasetVersion)
	}
	if evaluatorVersion != "" && evaluatorVersion != b.EvaluatorVersion {
		return fmt.Errorf("evaluation_setup_error: evaluator version %q does not match baseline %q", evaluatorVersion, b.EvaluatorVersion)
	}
	return nil
}

// Compare reads a run against the baseline. It refuses to compare across
// dataset, evaluator or report-schema changes: a comparison made across those
// boundaries is not a regression signal, it is two different measurements.
func (b Baseline) Compare(request ComparisonRequest) (BaselineComparison, error) {
	if err := b.Validate(); err != nil {
		return BaselineComparison{}, err
	}
	if err := b.CheckCompatible(request.DatasetName, request.DatasetVersion, request.EvaluatorVersion, request.ReportSchemaVersion); err != nil {
		return BaselineComparison{}, err
	}

	approved := make(map[string]BaselineCase, len(b.Cases))
	for _, item := range b.Cases {
		approved[item.CaseID] = item
	}
	current := make(map[string]CurrentVerdict, len(request.Cases))
	for _, item := range request.Cases {
		current[item.CaseID] = item
	}

	comparison := BaselineComparison{Outcomes: make([]CaseOutcome, 0, len(request.Cases)+len(b.Cases))}
	for _, item := range request.Cases {
		previous, known := approved[item.CaseID]
		switch {
		case !known:
			comparison.New = append(comparison.New, item.CaseID)
			comparison.Outcomes = append(comparison.Outcomes, CaseOutcome{CaseID: item.CaseID, Outcome: OutcomeNew})
		case previous.Passed && !item.Passed:
			comparison.Regressions = append(comparison.Regressions, item.CaseID)
			comparison.Outcomes = append(comparison.Outcomes, CaseOutcome{CaseID: item.CaseID, Outcome: OutcomeRegression})
		case !previous.Passed && !item.Passed:
			comparison.StillFailing = append(comparison.StillFailing, item.CaseID)
			comparison.Outcomes = append(comparison.Outcomes, CaseOutcome{CaseID: item.CaseID, Outcome: OutcomeStillFailing})
		default:
			comparison.Unchanged = append(comparison.Unchanged, item.CaseID)
			comparison.Outcomes = append(comparison.Outcomes, CaseOutcome{CaseID: item.CaseID, Outcome: OutcomeUnchanged})
		}
	}
	for _, item := range b.Cases {
		if _, ok := current[item.CaseID]; ok {
			continue
		}
		comparison.Missing = append(comparison.Missing, item.CaseID)
		comparison.Outcomes = append(comparison.Outcomes, CaseOutcome{CaseID: item.CaseID, Outcome: OutcomeMissing})
	}

	sort.Strings(comparison.Regressions)
	sort.Strings(comparison.Missing)
	sort.Strings(comparison.New)
	sort.Strings(comparison.StillFailing)
	sort.Strings(comparison.Unchanged)
	sort.Slice(comparison.Outcomes, func(i, j int) bool {
		if comparison.Outcomes[i].CaseID != comparison.Outcomes[j].CaseID {
			return comparison.Outcomes[i].CaseID < comparison.Outcomes[j].CaseID
		}
		return comparison.Outcomes[i].Outcome < comparison.Outcomes[j].Outcome
	})
	return comparison, nil
}

// NewBaseline builds an approved snapshot from a run, for recording the first
// baseline or deliberately re-approving one.
func NewBaseline(name, datasetName, datasetVersion, evaluatorVersion string, reportSchemaVersion int, approvedAt time.Time, cases []CurrentVerdict) Baseline {
	baseline := Baseline{
		BaselineName: name, DatasetName: datasetName, DatasetVersion: datasetVersion,
		EvaluatorVersion: evaluatorVersion, ReportSchemaVersion: reportSchemaVersion,
		ApprovedAt: approvedAt.UTC(),
	}
	for _, item := range cases {
		baseline.Cases = append(baseline.Cases, BaselineCase{
			CaseID: item.CaseID, CaseVersion: item.CaseVersion,
			Passed: item.Passed, VerdictDigest: item.VerdictDigest,
		})
	}
	sort.Slice(baseline.Cases, func(i, j int) bool { return baseline.Cases[i].CaseID < baseline.Cases[j].CaseID })
	return baseline
}

// WriteBaseline writes an approved snapshot as reviewable JSON. It is the only
// way a baseline enters the repository, so the file always comes from a real
// run rather than being hand-written.
func WriteBaseline(path string, baseline Baseline) error {
	if err := baseline.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// SameVerdicts reports whether two snapshots describe the same measurement:
// same dataset, evaluator and report schema version, and an identical per-case
// verdict identity (case, version, pass/fail and verdict digest). Re-approving
// an unchanged measurement can then keep the original approval time instead of
// producing a diff that says nothing.
//
// The digest is part of this identity because it is the verdict identity, not a
// request identity: it stays stable across independent runs of unchanged code,
// so including it keeps re-approval quiet in the normal case while making a
// changed observable behaviour visible as a fresh approval rather than as a
// silently rewritten snapshot. The regression gate itself stays pass/fail based
// (Compare), so a digest-only change never turns a green run red.
func (b Baseline) SameVerdicts(other Baseline) bool {
	if b.DatasetName != other.DatasetName || b.DatasetVersion != other.DatasetVersion ||
		b.EvaluatorVersion != other.EvaluatorVersion || b.ReportSchemaVersion != other.ReportSchemaVersion {
		return false
	}
	if len(b.Cases) != len(other.Cases) {
		return false
	}
	for index := range b.Cases {
		left, right := b.Cases[index], other.Cases[index]
		if left.CaseID != right.CaseID || left.CaseVersion != right.CaseVersion ||
			left.Passed != right.Passed || left.VerdictDigest != right.VerdictDigest {
			return false
		}
	}
	return true
}
