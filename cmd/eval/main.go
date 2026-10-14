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
	"sort"
	"strings"
	"time"

	eval "github.com/observer-mimiron/supervisor-template/eval"
	"github.com/observer-mimiron/supervisor-template/eval/evaluator"
	"github.com/observer-mimiron/supervisor-template/eval/langfuse"
	"github.com/observer-mimiron/supervisor-template/eval/runner"
	"github.com/observer-mimiron/supervisor-template/eval/semantic"
	"github.com/observer-mimiron/supervisor-template/internal/config"
	"github.com/observer-mimiron/supervisor-template/internal/infrastructure/llm"
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
	datasetPath := flags.String("dataset", "./eval/datasets/synthetic-operations-v5.json", "dataset path")
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
	// Offline semantic modes. They are separate command modes, not extra
	// evaluation steps: -calibrate replays registered statistics and never calls
	// a model, and it must not be combinable with a hard evaluation run.
	calibrate := flags.Bool("calibrate", false, "offline calibration replay over local materials, labels and predictions (never calls a model)")
	semanticMaterials := flags.String("semantic-materials", "", "local JSONL material file")
	semanticAnnotations := flags.String("semantic-annotations", "", "local JSONL human annotations")
	semanticReviews := flags.String("semantic-reviews", "", "optional local JSONL human reviews of those annotations")
	calibrationPredictions := flags.String("calibration-predictions", "", "candidate prediction JSONL path")
	calibrationConfig := flags.String("calibration-config", "", "pre-registered manifest JSON path")
	calibrationOutput := flags.String("calibration-output", "", "calibration report output path")
	calibrationSample := flags.Bool("calibration-sample", false, "explicitly collect uncalibrated Judge candidates for a first calibration")
	semanticJudge := flags.String("semantic-judge", string(semantic.JudgeModeOff), "semantic judge mode: off, fake or real")
	semanticBudget := flags.String("semantic-budget", "", "finite call/time/size budget JSON for the semantic judge")
	semanticCalibration := flags.String("semantic-calibration", "", "local calibration report that authorizes formal semantic scoring")
	if err := flags.Parse(args); err != nil {
		return exitSetup
	}
	provided := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	if *calibrate && *calibrationSample {
		fmt.Fprintln(stderr, "evaluation_setup_error: -calibrate and -calibration-sample are separate modes and cannot be combined")
		return exitSetup
	}
	if *calibrate || *calibrationSample {
		mode, modeErr := semantic.ParseJudgeMode(*semanticJudge)
		if modeErr != nil {
			fmt.Fprintln(stderr, modeErr)
			return exitSetup
		}
		if *calibrationSample {
			return runCalibrationSample(calibrationSampleOptions{
				judgeMode:       mode,
				materialsPath:   *semanticMaterials,
				configPath:      *calibrationConfig,
				budgetPath:      *semanticBudget,
				predictionsPath: *calibrationPredictions,
				serverConfig:    *configPath,
				codeVersion:     *codeVersion,
				provided:        provided,
			}, stdout, stderr)
		}
		return runCalibrate(calibrateOptions{
			materialsPath:   *semanticMaterials,
			annotationsPath: *semanticAnnotations,
			reviewsPath:     *semanticReviews,
			predictionsPath: *calibrationPredictions,
			configPath:      *calibrationConfig,
			outputPath:      *calibrationOutput,
			datasetPath:     *datasetPath,
			codeVersion:     *codeVersion,
			provided:        provided,
		}, stdout, stderr)
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
	// The optional semantic layer is assembled before the hard Cases run and can
	// never turn a semantic problem into a setup error: every failure path
	// degrades to a closed `unverified` reason and zero Judge calls.
	judgeMode, modeErr := semantic.ParseJudgeMode(*semanticJudge)
	if modeErr != nil {
		fmt.Fprintln(stderr, modeErr)
		return exitSetup
	}
	session := buildSemanticSession(semanticSessionOptions{
		mode: judgeMode, materialsPath: *semanticMaterials, budgetPath: *semanticBudget,
		calibrationPath: *semanticCalibration, serverConfig: *configPath, profile: caseRunner.Profile.Scenario,
	})
	cases := make([]evaluator.CaseReport, 0, len(selectedCases))
	for _, item := range selectedCases {
		evidence, runErr := caseRunner.RunCase(context.Background(), item)
		if runErr != nil {
			evidence.Error = runErr.Error()
		}
		report := evaluator.Evaluate(item, evidence)
		// Every schema-3 Case carries its semantic state, including the default
		// "not exercised" one, so a reader never has to infer whether the layer
		// was off, uncalibrated or failed.
		report.Semantic = evaluateCaseSemantics(context.Background(), session, item, evidence)
		cases = append(cases, report)
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
	triage, triageErr := evaluator.Triage(report)
	if triageErr != nil {
		fmt.Fprintln(stderr, triageErr)
		return exitSetup
	}
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

// hardEvaluationOptions are the flags that belong to a hard evaluation run. A
// semantic mode refuses them instead of silently ignoring them, so a mistyped
// command cannot look like a run that also produced a report or a baseline.
var hardEvaluationOptions = []string{
	"dataset", "report", "jsonl", "risk", "impact-tags", "baseline",
	"check-coverage", "triage", "write-baseline", "langfuse-upload", "langfuse-api-url",
}

type calibrateOptions struct {
	materialsPath   string
	annotationsPath string
	reviewsPath     string
	predictionsPath string
	configPath      string
	outputPath      string
	datasetPath     string
	codeVersion     string
	provided        map[string]bool
}

// runCalibrate replays the registered calibration statistics offline.
//
// It never constructs the runtime runner or a Judge, so it cannot make an
// outbound request even by accident. Malformed calibration input is a setup
// error (exit 2); a well-formed replay whose verdict is `unusable` is a
// successful command (exit 0), because a semantic conclusion must not become a
// hard evaluation failure.
func runCalibrate(options calibrateOptions, stdout, stderr io.Writer) int {
	for _, name := range hardEvaluationOptions {
		if options.provided[name] {
			fmt.Fprintf(stderr, "evaluation_setup_error: -%s belongs to a hard evaluation run and cannot be combined with -calibrate\n", name)
			return exitSetup
		}
	}
	for name, value := range map[string]string{
		"-semantic-materials":      options.materialsPath,
		"-semantic-annotations":    options.annotationsPath,
		"-calibration-predictions": options.predictionsPath,
		"-calibration-config":      options.configPath,
		"-calibration-output":      options.outputPath,
	} {
		if strings.TrimSpace(value) == "" {
			fmt.Fprintf(stderr, "evaluation_setup_error: -calibrate requires %s\n", name)
			return exitSetup
		}
	}

	materials, err := semantic.LoadMaterials(options.materialsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	annotations, err := semantic.LoadAnnotations(options.annotationsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	reviews := make([]semantic.Review, 0)
	if strings.TrimSpace(options.reviewsPath) != "" {
		reviews, err = semantic.LoadReviews(options.reviewsPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return exitSetup
		}
	}
	candidates, err := semantic.LoadCalibrationCandidates(options.predictionsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	manifest, err := semantic.LoadRegisteredManifest(options.configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	// The dataset is only used for the identity of what was evaluated. When it is
	// not given explicitly, the manifest's own registration remains the source of
	// truth for the registered thresholds.
	datasetIdentity := ""
	if strings.TrimSpace(options.datasetPath) != "" {
		if dataset, loadErr := eval.Load(options.datasetPath); loadErr == nil {
			datasetIdentity = dataset.Name + "@" + dataset.Version
		}
	}
	sourceDigest, err := calibrationSourceDigest()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	report, err := semantic.BuildCalibration(semantic.CalibrationInput{
		CalibrationID: manifest.ManifestID, Materials: materials, Annotations: annotations,
		Reviews: reviews, Candidates: candidates, Manifest: manifest,
		Dataset: datasetIdentity, EvaluatorVersion: evaluator.Version, Profile: "offline",
		CodeVersion: options.codeVersion, SourceDigest: sourceDigest, Now: time.Now(),
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	if err := semantic.WriteJSONSecret(options.outputPath, report); err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	fmt.Fprintf(stdout, "calibration status=%s path=%s materials=%d groups=%d paired=%d errors=%d skips=%d pending=%d\n",
		report.Acceptance.Status, options.outputPath, report.SampleFlow.UniqueMaterials,
		report.SampleFlow.IndependentGroups, report.SampleFlow.Paired, report.SampleFlow.Errors,
		report.SampleFlow.Skips, report.SampleFlow.Disagreements)
	if report.Acceptance.Status != semantic.AcceptanceUsable {
		fmt.Fprintf(stdout, "  not usable: %s\n", joinReasons(report.Acceptance.Reasons))
	}
	if report.HumanReliability.Status != semantic.EstimateEstimated {
		fmt.Fprintf(stdout, "  human reliability unverified: %s\n", report.HumanReliability.Reason)
	}
	return exitPassed
}

func joinReasons(reasons []semantic.AcceptanceReason) string {
	values := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		values = append(values, string(reason))
	}
	return strings.Join(values, ",")
}

// calibrationSourceDigest hashes the allow-listed minimal source set whose
// revision belongs in a calibration identity. The module root is located by
// walking up to go.mod so the command works from any directory inside the
// repository. A missing module root is a setup error: an identity with no source
// revision would silently compare equal to any other.
func calibrationSourceDigest() (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(filepath.Join(root, "eval", "semantic"))
	if err != nil {
		return "", err
	}
	paths := []string{filepath.Join(root, "eval", "evaluator", "evaluator.go")}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		paths = append(paths, filepath.Join(root, "eval", "semantic", name))
	}
	sort.Strings(paths)
	return semantic.SourceDigest(paths)
}

func moduleRoot() (string, error) {
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
			return "", fmt.Errorf("evaluation_setup_error: could not locate the module root above the working directory")
		}
		directory = parent
	}
}

// --- semantic session: shared assembly for the two modes that may call a Judge

// semanticSession is the assembled semantic layer for one run. Assembly never
// fails the run: a semantic problem degrades to a closed `unverified` reason,
// because the semantic layer must not be able to turn a hard evaluation into a
// setup error.
type semanticSession struct {
	mode        semantic.JudgeMode
	byCase      map[string]semantic.Material
	byKey       semantic.MaterialIndex
	runner      *semantic.BudgetedJudge
	reuse       semantic.ReuseCheck
	scorer      *semantic.ScorerIdentity
	calibration *semantic.CalibrationReport
	// Reason is the closed degradation reason when the layer is not usable.
	Reason semantic.ReasonCode
}

// buildSemanticSession assembles the optional semantic layer.
//
// Every failure path returns a session with a closed reason and no judge. It
// never returns an error, so a malformed optional semantic file cannot create a
// hard failure or a setup exit code.
func buildSemanticSession(options semanticSessionOptions) semanticSession {
	session := semanticSession{mode: options.mode, reuse: semantic.ReuseCheck{Reason: semantic.ReasonNotCalibrated}}
	if options.mode == semantic.JudgeModeOff {
		session.Reason = semantic.ReasonDisabled
		return session
	}
	if strings.TrimSpace(options.materialsPath) == "" {
		session.Reason = semantic.ReasonMaterialMissing
		return session
	}
	materials, err := semantic.LoadMaterials(options.materialsPath)
	if err != nil {
		session.Reason = semantic.ReasonOf(err)
		return session
	}
	index, err := semantic.IndexMaterials(materials)
	if err != nil {
		session.Reason = semantic.ReasonOf(err)
		return session
	}
	session.byKey = index
	session.byCase = map[string]semantic.Material{}
	for _, material := range materials {
		session.byCase[material.CaseID] = material
	}
	if strings.TrimSpace(options.budgetPath) == "" {
		session.Reason = semantic.ReasonBudgetMissing
		return session
	}
	budget, err := semantic.LoadBudget(options.budgetPath)
	if err != nil {
		session.Reason = semantic.ReasonOf(err)
		return session
	}
	if strings.TrimSpace(options.calibrationPath) == "" {
		// Formal scoring has no skip-calibration switch: without a matching usable
		// calibration there are zero calls, whatever the mode is.
		session.Reason = semantic.ReasonNotCalibrated
		return session
	}
	report, err := semantic.LoadCalibrationReport(options.calibrationPath)
	if err != nil {
		session.Reason = semantic.ReasonOf(err)
		return session
	}
	session.calibration = &report
	session.scorer = &report.Scorer
	current := semantic.CalibrationIdentity{
		SchemaVersion: "1", CalibrationID: report.Identity.CalibrationID,
		Dimension: report.Identity.Dimension, Dataset: report.Identity.Dataset,
		EvaluatorVersion: evaluator.Version, Profile: options.profile,
		ScorerKind: options.mode.Kind(), ScorerDigest: report.Identity.ScorerDigest,
		MaterialSetDigest:   semantic.MaterialSetDigest(materials),
		AnnotationSetDigest: report.Identity.AnnotationSetDigest,
		SplitDigest:         report.Identity.SplitDigest, RulesDigest: report.Identity.RulesDigest,
		EnvironmentDigest: report.Identity.EnvironmentDigest, SourceDigest: report.Identity.SourceDigest,
	}
	reuse := semantic.CheckCalibrationReuse(report, options.mode, report.Identity.ScorerDigest, current)
	session.reuse = reuse
	if !reuse.Allowed {
		session.Reason = reuse.Reason
		return session
	}
	judge, err := buildJudge(options.mode, options.serverConfig, budget)
	if err != nil {
		session.Reason = semantic.ReasonOf(err)
		return session
	}
	session.runner = &semantic.BudgetedJudge{Judge: judge, Budget: budget}
	return session
}

type semanticSessionOptions struct {
	mode            semantic.JudgeMode
	materialsPath   string
	budgetPath      string
	calibrationPath string
	serverConfig    string
	profile         string
}

// buildJudge constructs the configured judge. The fake is local and makes no
// external request; the real adapter reuses the existing model configuration and
// credential path.
func buildJudge(mode semantic.JudgeMode, serverConfig string, budget semantic.BudgetIdentity) (semantic.Judge, error) {
	switch mode {
	case semantic.JudgeModeFake:
		return &semantic.FakeJudge{}, nil
	case semantic.JudgeModeReal:
		cfg, err := config.Load(serverConfig)
		if err != nil {
			return nil, semantic.JudgeFailure(semantic.ReasonCredentialMissing, "judge configuration could not be read")
		}
		return llm.NewDeepSeekJudge(context.Background(), llm.JudgeOptions{
			Name: cfg.Model.Name, BaseURL: cfg.Model.BaseURL, APIKeyEnv: cfg.Model.APIKeyEnv,
			Temperature: cfg.Model.Temperature, MaxTokens: cfg.Model.MaxTokens, Timeout: cfg.Model.Timeout,
		}, budget.MaxOutputBytes)
	}
	return nil, semantic.JudgeFailure(semantic.ReasonDisabled, "semantic judge mode is off")
}

// --- explicit candidate sampling (FR-014 / SC-007)

type calibrationSampleOptions struct {
	judgeMode       semantic.JudgeMode
	materialsPath   string
	configPath      string
	budgetPath      string
	predictionsPath string
	serverConfig    string
	codeVersion     string
	provided        map[string]bool
}

// runCalibrationSample collects uncalibrated candidates for a first calibration.
//
// It is the only mode that may call a Judge without a usable calibration, and it
// buys that with the full guard set: registered manifest, approved materials,
// privacy projection and a finite budget. It loads no human truth, runs no hard
// Case, writes no report, baseline or formal label, uploads nothing, and never
// produces a usable authorization.
func runCalibrationSample(options calibrationSampleOptions, stdout, stderr io.Writer) int {
	for _, name := range hardEvaluationOptions {
		if options.provided[name] {
			fmt.Fprintf(stderr, "evaluation_setup_error: -%s belongs to a hard evaluation run and cannot be combined with -calibration-sample\n", name)
			return exitSetup
		}
	}
	if options.provided["semantic-annotations"] || options.provided["semantic-reviews"] || options.provided["semantic-calibration"] {
		fmt.Fprintln(stderr, "evaluation_setup_error: sampling must not load human labels or an existing calibration")
		return exitSetup
	}
	if options.judgeMode != semantic.JudgeModeFake && options.judgeMode != semantic.JudgeModeReal {
		fmt.Fprintln(stderr, "evaluation_setup_error: -calibration-sample requires -semantic-judge fake or real")
		return exitSetup
	}
	for name, value := range map[string]string{
		"-semantic-materials":      options.materialsPath,
		"-calibration-config":      options.configPath,
		"-semantic-budget":         options.budgetPath,
		"-calibration-predictions": options.predictionsPath,
	} {
		if strings.TrimSpace(value) == "" {
			fmt.Fprintf(stderr, "evaluation_setup_error: -calibration-sample requires %s\n", name)
			return exitSetup
		}
	}

	materials, err := semantic.LoadMaterials(options.materialsPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	manifest, err := semantic.LoadRegisteredManifest(options.configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	budget, err := semantic.LoadBudget(options.budgetPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	if manifest.Budget == nil || manifest.Budget.Digest != budget.Digest {
		fmt.Fprintln(stderr, "evaluation_setup_error: the registered budget does not match the manifest")
		return exitSetup
	}
	if len(manifest.ApprovedMaterials) == 0 {
		fmt.Fprintln(stderr, "evaluation_setup_error: the manifest must approve the materials to be sampled")
		return exitSetup
	}
	approved := map[string]semantic.ApprovedMaterial{}
	for _, entry := range manifest.ApprovedMaterials {
		approved[entry.SampleID+"|"+entry.MaterialDigest] = entry
	}
	// Approved-source, privacy and identity checks happen before any request.
	scorer := manifest.Scorer
	if manifest.Scorer.Digest != semantic.ScorerDigest(manifest.Scorer) {
		fmt.Fprintln(stderr, "evaluation_setup_error: the registered scorer digest does not match its content")
		return exitSetup
	}
	if manifest.Scorer.Kind != options.judgeMode.Kind() {
		fmt.Fprintln(stderr, "evaluation_setup_error: the requested judge mode does not match the registered scorer kind")
		return exitSetup
	}
	judge, err := buildJudge(options.judgeMode, options.serverConfig, budget)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}

	runner := &semantic.BudgetedJudge{Judge: judge, Budget: budget}
	records := make([]semantic.CalibrationCandidate, 0, len(materials))
	now := time.Now().UTC()
	ctx := context.Background()
	for _, material := range materials {
		entry, ok := approved[material.SampleID+"|"+material.MaterialDigest]
		if !ok {
			fmt.Fprintf(stderr, "evaluation_setup_error: material %s is not approved by the manifest\n", material.SampleID)
			return exitSetup
		}
		if entry.MaterialGroupID != material.MaterialGroupID {
			fmt.Fprintf(stderr, "evaluation_setup_error: material %s declares a different group than the manifest\n", material.SampleID)
			return exitSetup
		}
		if _, registered := manifest.Splits.SplitOf(material.MaterialGroupID); !registered {
			fmt.Fprintf(stderr, "evaluation_setup_error: material %s is outside the registered split plan\n", material.SampleID)
			return exitSetup
		}
		// Every planned invocation becomes exactly one record, so a failure can
		// never disappear from the denominator.
		invocation := runner.Invoke(ctx, material)
		record := semantic.CalibrationCandidate{
			SchemaVersion: semantic.CalibrationCandidateSchemaVersion, Purpose: semantic.CalibrationCandidatePurpose,
			ManifestDigest: manifest.Digest(), ScorerDigest: scorer.Digest, ScorerKind: scorer.Kind,
			SampleID: material.SampleID, MaterialGroupID: material.MaterialGroupID,
			MaterialDigest: material.MaterialDigest, Dimension: material.Dimension,
			InvocationIndex: semantic.PrimaryInvocationIndex, Outcome: invocation.Outcome,
			ReasonCode: invocation.Reason, RecordedAtUTC: now,
		}
		if invocation.Response != nil {
			label := invocation.Response.Label
			record.Prediction = &label
		}
		if err := record.Validate(); err != nil {
			fmt.Fprintln(stderr, err)
			return exitSetup
		}
		records = append(records, record)
	}
	// The candidate file is written to a new path only: overwriting would destroy
	// the denominator an earlier collection recorded.
	if err := semantic.WriteJSONLNew(options.predictionsPath, records); err != nil {
		fmt.Fprintln(stderr, err)
		return exitSetup
	}
	predictions, errors, skips := 0, 0, 0
	for _, record := range records {
		switch record.Outcome {
		case semantic.CandidatePrediction:
			predictions++
		case semantic.CandidateSkip:
			skips++
		default:
			errors++
		}
	}
	fmt.Fprintf(stdout, "calibration candidates path=%s planned=%d predictions=%d errors=%d skips=%d scorer=%s\n",
		options.predictionsPath, len(records), predictions, errors, skips, scorer.Kind)
	fmt.Fprintln(stdout, "  candidates are not human labels and do not authorize formal scoring; replay them with -calibrate")
	return exitPassed
}

// evaluateCaseSemantics produces one case's advisory semantic result.
//
// The material is matched by Case identity and the existing hard verdict digest,
// so a label can never be attached to a different answer. When a case has no
// independent material the result is `unverified/material_missing`, which is not
// a hard failure.
func evaluateCaseSemantics(ctx context.Context, session semanticSession, item eval.Case, evidence runner.Evidence) *semantic.Result {
	request := semantic.EvaluationRequest{
		Mode: session.mode, Reuse: session.reuse, Runner: session.runner, Unavailable: session.Reason,
	}
	if session.calibration != nil {
		request.CalibrationDigest = session.calibration.Identity.Digest
	}
	if material, ok := session.byCase[item.ID]; ok && material.CaseVersion == item.Version {
		// The material must describe the same observed hard verdict as this run;
		// otherwise it belongs to a different answer and reusing it would judge
		// the wrong text.
		if evidence.VerdictDigest == "" || material.VerdictDigest == evidence.VerdictDigest {
			request.Material = &material
		}
	}
	result := semantic.Evaluate(ctx, request)
	return &result
}
