// Command eval runs the versioned local acceptance Cases against the project's
// HTTP/SSE runtime entrypoint and writes a deterministic JSON/JSONL report.
//
// The PR default is offline and fake: the Runtime Runner forces the local fake
// model even when the process environment selects a real provider, and the
// report carries an explicit runtime-fake EvaluationProfile. A profile that does
// not describe the executor that actually ran is an evaluation_setup_error and
// never produces a report.
//
// This command only wires configuration, Case selection, the Runtime Runner and
// the report writers. It does not compute verdicts (eval/evaluator owns them) and
// owns no Runtime state, Policy, Approval, Idempotency or terminal events, which
// stay in the application.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	eval "github.com/observer-mimiron/supervisor-template/eval"
	"github.com/observer-mimiron/supervisor-template/eval/evaluator"
	"github.com/observer-mimiron/supervisor-template/eval/langfuse"
	"github.com/observer-mimiron/supervisor-template/eval/runner"
)

const (
	exitPassed = 0
	exitFailed = 1
	exitSetup  = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// newCaseRunner is the composition seam for the CLI. Tests replace it to prove
// that a profile/executor mismatch is rejected before any report is written.
var newCaseRunner = func(configPath, codeVersion string, observability bool) *runner.Runner {
	if observability {
		return runner.NewWithObservability(configPath, codeVersion, runner.DefaultToken)
	}
	return runner.New(configPath, codeVersion, runner.DefaultToken)
}

// run is the testable entrypoint. It returns 0 when every selected Case passes,
// 1 when a deterministic assertion fails, and 2 for a dataset, selection,
// profile or report setup error.
func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	datasetPath := flags.String("dataset", "./eval/datasets/synthetic-operations-v2.json", "dataset path")
	reportPath := flags.String("report", "./tmp/eval-report.json", "report path")
	jsonlPath := flags.String("jsonl", "", "optional JSONL report path")
	codeVersion := flags.String("code-version", "dev", "code version")
	configPath := flags.String("config", "./config.example.toml", "server config path")
	riskLevel := flags.String("risk", os.Getenv("EVAL_RISK_LEVEL"), "declared PR risk: low, medium, high, or unknown")
	impactTags := flags.String("impact-tags", os.Getenv("EVAL_IMPACT_TAGS"), "comma-separated impact tags")
	baselinePath := flags.String("baseline", "", "optional approved baseline snapshot for case-level regression gating")
	coveragePath := flags.String("check-coverage", "", "validate the claim-to-Case coverage file against the dataset, then exit")
	triagePath := flags.String("triage", "", "optional path for a machine-readable failure triage")
	writeBaselinePath := flags.String("write-baseline", "", "write an approved baseline snapshot from this run (refused when the run has failures)")
	langfuseUpload := flags.Bool("langfuse-upload", false, "optionally upload trace-linked scores to Langfuse")
	langfuseAPIURL := flags.String("langfuse-api-url", os.Getenv("LANGFUSE_API_URL"), "Langfuse API base URL for optional score upload")
	if err := flags.Parse(args); err != nil {
		return exitSetup
	}

	dataset, err := eval.Load(*datasetPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	// Coverage is a pure referential check: every claim must point at Cases that
	// exist, or be explicitly waived. It runs before anything else so a claim
	// that nothing verifies cannot hide behind a green Case run.
	if *coveragePath != "" {
		coverage, err := eval.LoadCoverage(*coveragePath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitSetup
		}
		coverageReport, err := eval.CheckCoverage(coverage, dataset)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitSetup
		}
		fmt.Fprintln(stdout, coverageReport.Summary())
		if len(coverageReport.Unreferenced) > 0 {
			fmt.Fprintf(stdout, "  cases without a claim: %s\n", strings.Join(coverageReport.Unreferenced, ", "))
		}
		return exitPassed
	}
	// Baseline compatibility is checked before any Case runs: an incomparable
	// run is a setup error and must not leave a report behind.
	var baseline eval.Baseline
	if *baselinePath != "" {
		loaded, err := eval.LoadBaseline(*baselinePath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitSetup
		}
		if err := loaded.CheckCompatible(dataset.Name, dataset.Version, evaluator.Version, evaluator.ReportSchemaVersion); err != nil {
			fmt.Fprintln(stderr, err)
			return exitSetup
		}
		baseline = loaded
	}
	selection := eval.Selection{RiskLevel: *riskLevel, ImpactTags: eval.NormalizeImpactTags(*impactTags)}
	selectedCases, err := eval.SelectCases(dataset, selection)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	// Reject a profile that does not describe the executor before running Cases
	// or writing a report, so a mislabelled run cannot leave report artifacts.
	caseRunner := newCaseRunner(*configPath, *codeVersion, *langfuseUpload)
	if err := caseRunner.ValidateProfile(); err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	if err := os.MkdirAll(filepath.Dir(*reportPath), 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	cases := make([]evaluator.CaseReport, 0, len(selectedCases))
	for _, item := range selectedCases {
		evidence, runErr := caseRunner.RunCase(context.Background(), item)
		if runErr != nil {
			evidence.Error = runErr.Error()
		}
		cases = append(cases, evaluator.Evaluate(item, evidence))
	}
	profile := caseRunner.Profile
	report := evaluator.BuildWithProfile(dataset, *codeVersion, selection, profile, cases)
	if err := evaluator.WriteJSON(*reportPath, report); err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	if *jsonlPath != "" {
		if err := evaluator.WriteJSONL(*jsonlPath, report); err != nil {
			fmt.Fprintln(stderr, err)
			return exitSetup
		}
	}
	triage := evaluator.Triage(report)
	if len(triage) > 0 {
		if *triagePath != "" {
			if err := evaluator.WriteTriage(*triagePath, triage); err != nil {
				fmt.Fprintln(stderr, err)
				return exitSetup
			}
		}
		fmt.Fprint(stdout, evaluator.RenderTriage(triage))
	}
	regressed := false
	if *writeBaselinePath != "" {
		if report.Failed > 0 {
			fmt.Fprintf(stderr, "refusing to write a baseline from a run with %d failing case(s)\n", report.Failed)
			return exitSetup
		}
		approvedAt := time.Now()
		// Re-approving an unchanged measurement keeps the original approval time,
		// so regenerating a baseline produces no diff unless something changed.
		if existing, loadErr := eval.LoadBaseline(*writeBaselinePath); loadErr == nil {
			candidate := eval.NewBaseline(report.DatasetName, report.DatasetName, report.DatasetVersion, evaluator.Version, report.ReportSchemaVersion, approvedAt, currentVerdicts(report))
			if candidate.SameVerdicts(existing) {
				approvedAt = existing.ApprovedAt
			}
		}
		baseline := eval.NewBaseline(report.DatasetName, report.DatasetName, report.DatasetVersion, evaluator.Version, report.ReportSchemaVersion, approvedAt, currentVerdicts(report))
		if err := eval.WriteBaseline(*writeBaselinePath, baseline); err != nil {
			fmt.Fprintln(stderr, err)
			return exitSetup
		}
		fmt.Fprintf(stdout, "baseline written path=%s cases=%d\n", *writeBaselinePath, len(baseline.Cases))
	}
	if *baselinePath != "" {
		comparison, err := baseline.Compare(eval.ComparisonRequest{
			DatasetName: report.DatasetName, DatasetVersion: report.DatasetVersion,
			EvaluatorVersion: evaluator.Version, ReportSchemaVersion: report.ReportSchemaVersion,
			Cases: currentVerdicts(report),
		})
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitSetup
		}
		fmt.Fprintln(stdout, comparison.Summary())
		for _, outcome := range comparison.Outcomes {
			if outcome.Outcome == eval.OutcomeUnchanged {
				continue
			}
			fmt.Fprintf(stdout, "  %-32s %s\n", outcome.CaseID, outcome.Outcome)
		}
		regressed = comparison.Failed()
	}
	if *langfuseUpload {
		client, clientErr := langfuse.New(*langfuseAPIURL, os.Getenv("LANGFUSE_PUBLIC_KEY"), os.Getenv("LANGFUSE_SECRET_KEY"))
		if clientErr != nil {
			fmt.Fprintf(stderr, "langfuse score upload skipped: %v\n", clientErr)
		} else if uploaded, uploadErr := client.UploadReport(context.Background(), report); uploadErr != nil {
			fmt.Fprintf(stderr, "langfuse score upload warning: %v\n", uploadErr)
		} else {
			fmt.Fprintf(stdout, "langfuse scores uploaded=%d\n", uploaded)
		}
	}
	fmt.Fprintf(stdout, "evaluation dataset=%s@%s scenario=%s cases=%d passed=%d failed=%d\n", report.DatasetName, report.DatasetVersion, report.EvaluationProfile.Scenario, len(report.Cases), report.Passed, report.Failed)
	for _, item := range report.Cases {
		status := "PASS"
		if !item.Passed {
			status = "FAIL"
		}
		fmt.Fprintf(stdout, "  %-32s %s terminal=%s writes=%d\n", item.CaseID, status, item.Evidence.Terminal, item.Evidence.FakeWriteCount)
	}
	if report.Failed > 0 || regressed {
		return exitFailed
	}
	return exitPassed
}

// currentVerdicts projects the report onto the minimal shape a baseline
// comparison needs, keeping eval/baseline.go free of evaluator imports. The
// per-Case verdict digest is carried along so an approved snapshot records the
// verdict identity it was approved on, not just a pass/fail label.
func currentVerdicts(report evaluator.Report) []eval.CurrentVerdict {
	verdicts := make([]eval.CurrentVerdict, 0, len(report.Cases))
	for _, item := range report.Cases {
		verdicts = append(verdicts, eval.CurrentVerdict{
			CaseID: item.CaseID, CaseVersion: item.CaseVersion,
			Passed: item.Passed, VerdictDigest: item.Evidence.VerdictDigest,
		})
	}
	return verdicts
}
