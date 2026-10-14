package evaluator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	eval "github.com/observer-mimiron/supervisor-template/eval"
)

// The frozen hard projection is the pre-migration reference for the controlled
// report-schema 2 -> 3 migration. It was captured from a real
// `synthetic-operations@5` runtime-fake run before any 005 implementation change,
// so "unchanged" is defined by the old contract rather than by the new code.
//
// Usage:
//
//	# compare any captured report against the frozen projection
//	EVAL_HARD_PROJECTION_REPORT=<captured-report.json> \
//	  go test ./eval/evaluator/ -run TestFrozenHardProjection
//
// The two captured reports used for the 2 -> 3 migration were deleted together
// with the rest of tmp/, and the current code only emits schema 3, so a fresh
// checkout has no default input. Without the environment variable the migration
// half is reported as not run rather than silently passing.

const frozenProjectionPath = "testdata/report-v2-hard-projection.json"
const frozenProjectionEnv = "EVAL_HARD_PROJECTION_REPORT"

type projectedDimension struct {
	Evaluator        string `json:"evaluator_name"`
	Version          string `json:"evaluator_version"`
	Passed           bool   `json:"passed"`
	Reason           string `json:"reason"`
	EvidenceRef      string `json:"evidence_reference"`
	FailedAssertion  string `json:"failed_assertion"`
	FailureTaxonomy  string `json:"failure_taxonomy"`
	HumanDisposition any    `json:"human_disposition"`
}

type projectedFailure struct {
	CaseID           string `json:"case_id"`
	CaseVersion      string `json:"case_version"`
	Evaluator        string `json:"evaluator_name"`
	Version          string `json:"evaluator_version"`
	FailedAssertion  string `json:"failed_assertion"`
	EvidenceRef      string `json:"evidence_reference"`
	HumanDisposition any    `json:"human_disposition"`
}

type projectedCase struct {
	CaseID              string               `json:"case_id"`
	CaseVersion         string               `json:"case_version"`
	Passed              bool                 `json:"passed"`
	VerdictDigest       string               `json:"verdict_digest"`
	RunTerminal         string               `json:"run_terminal"`
	EventTypes          []string             `json:"event_types"`
	EventCount          int                  `json:"event_count"`
	Result              map[string]any       `json:"result"`
	FakeWriteCount      int                  `json:"fake_write_count"`
	CleanupResult       string               `json:"cleanup_result"`
	HTTPStatuses        []int                `json:"http_statuses"`
	Retries             int                  `json:"retries"`
	RegisteredTools     []string             `json:"registered_tools"`
	WorkerToolAllowList map[string][]string  `json:"worker_tool_allow_list"`
	Dimensions          []projectedDimension `json:"dimensions"`
	Failures            []projectedFailure   `json:"failures"`
}

type frozenProjection struct {
	ProjectionVersion string          `json:"projection_version"`
	Cases             []projectedCase `json:"cases"`
}

// elapsedMS matches the one volatile value that is embedded in human-readable
// reason text. It is normalized rather than dropped so the rest of the reason
// still participates in the comparison.
var elapsedMS = regexp.MustCompile(`elapsed_ms=[0-9]+`)

// projectHard normalizes a report into the frozen hard projection.
//
// Included: case identity/version, the hard pass flag, the existing
// verdict_digest, per-dimension results and failure classifications, and stable
// run evidence (terminal, event types, projected result, write count, cleanup,
// HTTP statuses, retries, registration projection).
//
// Excluded as volatile or out of scope: generated_at, run identity, trace IDs,
// elapsed_ms, event IDs, and every envelope field added by the schema 3
// migration including the semantic object.
func projectHard(report Report) frozenProjection {
	projection := frozenProjection{ProjectionVersion: "1", Cases: make([]projectedCase, 0, len(report.Cases))}
	cases := append([]CaseReport(nil), report.Cases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].CaseID < cases[j].CaseID })
	for _, item := range cases {
		projected := projectedCase{
			CaseID: item.CaseID, CaseVersion: item.CaseVersion, Passed: item.Passed,
			VerdictDigest: item.Evidence.VerdictDigest, RunTerminal: item.Evidence.Terminal,
			EventTypes: make([]string, 0, len(item.Evidence.Events)), EventCount: len(item.Evidence.Events),
			Result: item.Evidence.Result, FakeWriteCount: item.Evidence.FakeWriteCount,
			CleanupResult: item.Evidence.CleanupResult, HTTPStatuses: item.Evidence.HTTPStatus,
			Retries: item.Evidence.Retries, RegisteredTools: append([]string(nil), item.Evidence.RegisteredTools...),
			WorkerToolAllowList: item.Evidence.WorkerToolAllowList,
			Dimensions:          make([]projectedDimension, 0, len(item.Results)),
			Failures:            make([]projectedFailure, 0, len(item.Failures)),
		}
		for _, event := range item.Evidence.Events {
			projected.EventTypes = append(projected.EventTypes, event.Type)
		}
		sort.Strings(projected.RegisteredTools)
		for _, result := range item.Results {
			projected.Dimensions = append(projected.Dimensions, projectedDimension{
				Evaluator: result.Evaluator, Version: result.Version, Passed: result.Passed,
				Reason:      elapsedMS.ReplaceAllString(result.Reason, "elapsed_ms=*"),
				EvidenceRef: result.EvidenceRef, FailedAssertion: result.FailedAssertion,
				FailureTaxonomy: result.FailureTaxonomy, HumanDisposition: dispositionValue(result.HumanDisposition),
			})
		}
		for _, record := range item.Failures {
			projected.Failures = append(projected.Failures, projectedFailure{
				CaseID: record.CaseID, CaseVersion: record.CaseVersion, Evaluator: record.Evaluator,
				Version: record.EvaluatorVersion, FailedAssertion: record.FailedAssertion,
				EvidenceRef: record.EvidenceReference, HumanDisposition: dispositionValue(record.HumanDisposition),
			})
		}
		projection.Cases = append(projection.Cases, projected)
	}
	return projection
}

func loadFrozenProjection(t *testing.T) frozenProjection {
	t.Helper()
	data, err := os.ReadFile(frozenProjectionPath)
	if err != nil {
		t.Fatalf("frozen projection: %v", err)
	}
	var frozen frozenProjection
	if err := json.Unmarshal(data, &frozen); err != nil {
		t.Fatalf("decode frozen projection: %v", err)
	}
	return frozen
}

// The frozen artifact itself is checked unconditionally: it must describe the 13
// hard Cases and it must not contain any volatile field, otherwise the migration
// comparison would be comparing noise.
func TestFrozenHardProjectionIsComplete(t *testing.T) {
	frozen := loadFrozenProjection(t)
	if frozen.ProjectionVersion != "1" {
		t.Fatalf("unexpected projection version %q", frozen.ProjectionVersion)
	}
	if len(frozen.Cases) != 13 {
		t.Fatalf("the frozen projection must cover the 13 hard Cases, got %d", len(frozen.Cases))
	}
	for _, item := range frozen.Cases {
		if item.CaseID == "" || item.CaseVersion == "" || item.VerdictDigest == "" {
			t.Fatalf("case identity must be part of the projection: %+v", item)
		}
		if len(item.Dimensions) != 4 {
			t.Fatalf("all four hard dimensions must be projected: %+v", item)
		}
		// A request-error Case legitimately has no events, so "evidence was
		// captured" is the invariant, not "events exist".
		if item.EventCount == 0 && item.Result == nil && len(item.HTTPStatuses) == 0 {
			t.Fatalf("stable run evidence must be projected: %+v", item)
		}
		for _, dimension := range item.Dimensions {
			if dimension.Version == "" {
				t.Fatalf("dimension versions are part of the hard contract: %+v", dimension)
			}
		}
	}
	// Only the projected cases are checked: projection_rules documents the
	// excluded field names on purpose, so scanning the whole file would match its
	// own documentation.
	data, err := json.Marshal(frozen.Cases)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, volatile := range []string{"trace_id", "generated_at", "elapsed_ms\":", "event_id", "run_id\":"} {
		if strings.Contains(text, volatile) {
			t.Fatalf("the frozen projection must not carry the volatile field %q", volatile)
		}
	}
}

// The migration comparison. It runs only when a report path is supplied; a
// missing artifact is reported as not run instead of passing quietly.
func TestFrozenHardProjectionIsUnchanged(t *testing.T) {
	path := os.Getenv(frozenProjectionEnv)
	if path == "" {
		t.Skipf("set %s to a captured report to run the migration comparison", frozenProjectionEnv)
	}
	// The test's working directory is this package, so a repository-relative path
	// is resolved against the module root.
	if !filepath.IsAbs(path) {
		root, err := moduleRootDir()
		if err != nil {
			t.Fatalf("resolve %s: %v", path, err)
		}
		path = filepath.Join(root, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("captured report %s: %v", path, err)
	}
	var report Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode captured report: %v", err)
	}
	frozen := loadFrozenProjection(t)
	actual := projectHard(report)
	if len(actual.Cases) != len(frozen.Cases) {
		t.Fatalf("case count changed: got %d, want %d", len(actual.Cases), len(frozen.Cases))
	}
	changed := 0
	for index := range frozen.Cases {
		want, got := frozen.Cases[index], actual.Cases[index]
		if reflect.DeepEqual(want, got) {
			continue
		}
		changed++
		t.Errorf("case %s changed:\n  want %s\n  got  %s", want.CaseID, describe(want), describe(got))
	}
	if changed == 0 {
		t.Logf("hard projection unchanged for %d/%d cases (report schema %d)", len(frozen.Cases), len(actual.Cases), report.ReportSchemaVersion)
	}
}

// dispositionValue dereferences the optional disposition so an absent value is an
// untyped nil. A typed nil pointer and a decoded JSON null are not DeepEqual,
// which would make every case look changed.
func dispositionValue(disposition *eval.HumanDisposition) any {
	if disposition == nil {
		return nil
	}
	return *disposition
}

func describe(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

// moduleRootDir walks up from the working directory to the module root, so a
// repository-relative artifact path works regardless of the test's directory.
func moduleRootDir() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(directory, "go.mod")); statErr == nil {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", fmt.Errorf("module root not found above the working directory")
		}
		directory = parent
	}
}
