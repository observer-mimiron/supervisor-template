package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	eval "github.com/observer-mimiron/supervisor-template/eval"
	"github.com/observer-mimiron/supervisor-template/eval/evaluator"
	"github.com/observer-mimiron/supervisor-template/eval/runner"
	"github.com/observer-mimiron/supervisor-template/eval/semantic"
)

const testDataset = `{
  "name": "cli-contract",
  "version": "1",
  "description": "CLI contract fixture with one passing read-only Case",
  "cases": [
    {
      "id": "cli-read-only",
      "version": "1",
      "category": "positive",
      "risk_level": "low",
      "impact_tags": ["cli"],
      "business_goal": "read-only request completes deterministically",
      "preconditions": {"fixture": "synthetic_audience_v1", "subject": "demo-user"},
      "request_steps": [{"action": "chat", "message": "分析沉睡客户", "conversation_id": "cli-read-only"}],
      "expected_results": {"terminal": "completed", "event_types": ["started", "decision", "plan", "progress", "tool_call", "progress", "text", "completed"], "result": {"count": 4}},
      "forbidden_side_effects": {"tool_ids": ["simulated_outreach"], "max_writes": 0},
      "evidence_requirements": ["events", "tool_calls", "run_state", "trace_correlation", "cleanup", "http_statuses", "sequence"],
      "evaluator_rules": ["business_correctness@1", "architecture_boundary@1", "side_effect_safety@1", "stability@1"],
      "timeout": "5s",
      "retry_budget": 1,
      "idempotency_key": "cli-read-only",
      "cleanup_policy": "isolated_run"
    }
  ]
}`

func writeTestDataset(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dataset.json")
	if err := os.WriteFile(path, []byte(testDataset), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readReport(t *testing.T, path string) evaluator.Report {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report evaluator.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	return report
}

// A fake Case must produce the same verdict on every independent run, with no
// model credential, network, or Langfuse sink available.
func TestRunProducesSameVerdictAcrossIndependentRuns(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "fake")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("LANGFUSE_API_URL", "")
	t.Setenv("LANGFUSE_PUBLIC_KEY", "")
	t.Setenv("LANGFUSE_SECRET_KEY", "")

	datasetPath := writeTestDataset(t)
	runOnce := func(name string) evaluator.Report {
		reportPath := filepath.Join(t.TempDir(), name+".json")
		var stdout, stderr bytes.Buffer
		if code := run([]string{
			"-dataset", datasetPath,
			"-report", reportPath,
			"-code-version", "determinism-check",
			"-config", "../../config.example.toml",
		}, &stdout, &stderr); code != exitPassed {
			t.Fatalf("run %s exit=%d stderr=%s", name, code, stderr.String())
		}
		return readReport(t, reportPath)
	}

	first := runOnce("first")
	second := runOnce("second")

	if first.EvaluationProfile != second.EvaluationProfile && !reflect.DeepEqual(first.EvaluationProfile, second.EvaluationProfile) {
		t.Fatalf("profile changed between runs: %#v vs %#v", first.EvaluationProfile, second.EvaluationProfile)
	}
	if first.Passed != second.Passed || first.Failed != second.Failed {
		t.Fatalf("verdict totals changed: %d/%d vs %d/%d", first.Passed, first.Failed, second.Passed, second.Failed)
	}
	if len(first.Cases) != len(second.Cases) || len(first.Cases) != 1 {
		t.Fatalf("case counts differ: %d vs %d", len(first.Cases), len(second.Cases))
	}
	left, right := first.Cases[0], second.Cases[0]
	if left.Passed != right.Passed {
		t.Fatalf("case verdict changed: %v vs %v", left.Passed, right.Passed)
	}
	if left.Evidence.Terminal != right.Evidence.Terminal {
		t.Fatalf("terminal changed: %q vs %q", left.Evidence.Terminal, right.Evidence.Terminal)
	}
	if left.Evidence.VerdictDigest == "" || left.Evidence.VerdictDigest != right.Evidence.VerdictDigest {
		t.Fatalf("verdict digest changed: %q vs %q", left.Evidence.VerdictDigest, right.Evidence.VerdictDigest)
	}
	if left.Evidence.FakeWriteCount != right.Evidence.FakeWriteCount || left.Evidence.CleanupResult != right.Evidence.CleanupResult {
		t.Fatalf("side-effect or cleanup evidence changed: %d/%s vs %d/%s", left.Evidence.FakeWriteCount, left.Evidence.CleanupResult, right.Evidence.FakeWriteCount, right.Evidence.CleanupResult)
	}
	if leftVerdicts, rightVerdicts := verdictSummary(left), verdictSummary(right); !reflect.DeepEqual(leftVerdicts, rightVerdicts) {
		t.Fatalf("per-evaluator verdicts changed:\n%v\n%v", leftVerdicts, rightVerdicts)
	}
	if len(left.Failures) != 0 || len(right.Failures) != 0 {
		t.Fatalf("deterministic failures reported for a passing case: %v / %v", left.Failures, right.Failures)
	}
}

// verdictSummary reduces a Case report to the deterministic pass/fail decision
// per evaluator so two runs can be compared without timestamps or trace IDs.
func verdictSummary(item evaluator.CaseReport) []string {
	summary := make([]string, 0, len(item.Results))
	for _, result := range item.Results {
		summary = append(summary, fmt.Sprintf("%s@%s=%t/%s", result.Evaluator, result.Version, result.Passed, result.FailedAssertion))
	}
	sort.Strings(summary)
	return summary
}

// The CLI must stay on the fake runtime even when the process environment
// selects a real provider, and the written report must say so explicitly.
func TestRunForcesFakeProfileRegardlessOfProviderEnvironment(t *testing.T) {
	t.Setenv("MODEL_PROVIDER", "deepseek")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("LLM_MODEL", "deepseek-chat")

	reportPath := filepath.Join(t.TempDir(), "report.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-dataset", writeTestDataset(t),
		"-report", reportPath,
		"-code-version", "cli-test",
		"-config", "../../config.example.toml",
	}, &stdout, &stderr)
	if code != exitPassed {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	report := readReport(t, reportPath)
	want := eval.RuntimeFakeProfile(eval.TierPR)
	if !reflect.DeepEqual(report.EvaluationProfile, want) {
		t.Fatalf("profile=%#v, want %#v", report.EvaluationProfile, want)
	}
	if report.Passed != 1 || report.Failed != 0 {
		t.Fatalf("passed=%d failed=%d, want 1/0", report.Passed, report.Failed)
	}
	if !strings.Contains(stdout.String(), "scenario=runtime-fake") || !strings.Contains(stdout.String(), "passed=1 failed=0") {
		t.Fatalf("summary does not report the local verdict and scenario: %s", stdout.String())
	}
}

func TestRunRejectsSetupErrorsBeforeWritingReport(t *testing.T) {
	t.Run("missing dataset", func(t *testing.T) {
		reportPath := filepath.Join(t.TempDir(), "nested", "report.json")
		var stdout, stderr bytes.Buffer
		code := run([]string{
			"-dataset", filepath.Join(t.TempDir(), "absent.json"),
			"-report", reportPath,
			"-config", "../../config.example.toml",
		}, &stdout, &stderr)
		if code != exitSetup {
			t.Fatalf("exit=%d, want %d", code, exitSetup)
		}
		if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
			t.Fatalf("setup error still wrote a report: %v", err)
		}
	})

	t.Run("selection produced no cases", func(t *testing.T) {
		// Declaring low risk against a high-risk-only dataset is a setup error,
		// not an empty passing report.
		path := filepath.Join(t.TempDir(), "dataset.json")
		highRisk := strings.Replace(testDataset, `"risk_level": "low"`, `"risk_level": "high"`, 1)
		if err := os.WriteFile(path, []byte(highRisk), 0o600); err != nil {
			t.Fatal(err)
		}
		reportPath := filepath.Join(t.TempDir(), "report.json")
		var stdout, stderr bytes.Buffer
		code := run([]string{
			"-dataset", path,
			"-report", reportPath,
			"-risk", "low",
			"-config", "../../config.example.toml",
		}, &stdout, &stderr)
		if code != exitSetup {
			t.Fatalf("exit=%d, want %d (stderr=%s)", code, exitSetup, stderr.String())
		}
		if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
			t.Fatalf("invalid selection still wrote a report: %v", err)
		}
	})

	t.Run("executor profile mismatch", func(t *testing.T) {
		original := newCaseRunner
		defer func() { newCaseRunner = original }()
		newCaseRunner = func(configPath, codeVersion string, observability bool) *runner.Runner {
			mismatched := original(configPath, codeVersion, observability)
			mismatched.Profile = eval.EvaluationProfile{
				EvaluationPlane: eval.PlaneRuntime, Scenario: eval.ScenarioRuntimeReal,
				ModelProvider: eval.String(eval.ProviderDeepSeek), ModelName: eval.String("deepseek-chat"),
				Executor: eval.String(eval.ExecutorLocalFake), Verifier: eval.String(eval.VerifierDeterministic),
				NetworkMode: eval.NetworkEnabled, Tier: eval.TierNightly,
			}
			return mismatched
		}
		reportPath := filepath.Join(t.TempDir(), "report.json")
		var stdout, stderr bytes.Buffer
		code := run([]string{
			"-dataset", writeTestDataset(t),
			"-report", reportPath,
			"-config", "../../config.example.toml",
		}, &stdout, &stderr)
		if code != exitSetup {
			t.Fatalf("exit=%d, want %d (stderr=%s)", code, exitSetup, stderr.String())
		}
		if !strings.Contains(stderr.String(), "evaluation_setup_error") {
			t.Fatalf("stderr=%s, want evaluation_setup_error", stderr.String())
		}
		if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
			t.Fatalf("mismatched profile still wrote a report: %v", err)
		}
	})
}

// An optional Langfuse sink must never change the local verdict, the exit code,
// or the fake profile recorded in the report.
func TestLangfuseFailureDoesNotChangeLocalVerdict(t *testing.T) {
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "sink unavailable", http.StatusInternalServerError)
	}))
	defer sink.Close()

	for _, test := range []struct {
		name        string
		apiURL      string
		publicKey   string
		secretKey   string
		wantMessage string
	}{
		{name: "missing credentials", apiURL: "", publicKey: "", secretKey: "", wantMessage: "skipped"},
		{name: "sink http failure", apiURL: sink.URL, publicKey: "pk", secretKey: "sk", wantMessage: "warning"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("LANGFUSE_API_URL", test.apiURL)
			t.Setenv("LANGFUSE_PUBLIC_KEY", test.publicKey)
			t.Setenv("LANGFUSE_SECRET_KEY", test.secretKey)

			reportPath := filepath.Join(t.TempDir(), "report.json")
			var stdout, stderr bytes.Buffer
			code := run([]string{
				"-dataset", writeTestDataset(t),
				"-report", reportPath,
				"-jsonl", reportPath + "l",
				"-config", "../../config.example.toml",
				"-langfuse-upload",
			}, &stdout, &stderr)
			if code != exitPassed {
				t.Fatalf("exit=%d stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.wantMessage) {
				t.Fatalf("stderr=%s, want %q", stderr.String(), test.wantMessage)
			}
			report := readReport(t, reportPath)
			if !reflect.DeepEqual(report.EvaluationProfile, eval.RuntimeFakeProfile(eval.TierPR)) {
				t.Fatalf("sink failure changed the profile: %#v", report.EvaluationProfile)
			}
			if report.Passed != 1 || report.Failed != 0 {
				t.Fatalf("sink failure changed the verdict: passed=%d failed=%d", report.Passed, report.Failed)
			}
		})
	}
}

// writeBrokenDataset keeps the dataset identity but breaks one expectation, so
// the Case that passes in the baseline now fails.
func writeBrokenDataset(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "broken.json")
	broken := strings.Replace(testDataset, `"result": {"count": 4}`, `"result": {"count": 999}`, 1)
	if broken == testDataset {
		t.Fatal("test dataset no longer contains the expected result to break")
	}
	if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunBaselineGatesOnCaseLevelRegression(t *testing.T) {
	dir := t.TempDir()
	datasetPath := writeTestDataset(t)
	baselinePath := filepath.Join(dir, "baseline.json")
	reportPath := filepath.Join(dir, "report.json")

	// A baseline may only be recorded from a real passing run.
	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"-dataset", datasetPath, "-report", reportPath, "-code-version", "baseline",
		"-config", "../../config.example.toml", "-write-baseline", baselinePath,
	}, &stdout, &stderr); code != exitPassed {
		t.Fatalf("baseline recording exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "baseline written") {
		t.Fatalf("baseline was not reported as written: %s", stdout.String())
	}
	// The recorded snapshot must carry the verdict identity, not just labels.
	recorded, err := os.ReadFile(baselinePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(recorded), `"verdict_digest": "`) {
		t.Fatalf("recorded baseline carries no verdict digests:\n%s", recorded)
	}
	stdout.Reset()
	stderr.Reset()

	// An unchanged run stays green.
	if code := run([]string{
		"-dataset", datasetPath, "-report", reportPath, "-code-version", "same",
		"-config", "../../config.example.toml", "-baseline", baselinePath,
	}, &stdout, &stderr); code != exitPassed {
		t.Fatalf("unchanged run exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "regressions=0") {
		t.Fatalf("unchanged run did not report a clean comparison: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()

	// A previously passing Case that now fails must fail the gate and name it.
	if code := run([]string{
		"-dataset", writeBrokenDataset(t), "-report", reportPath, "-code-version", "regressed",
		"-config", "../../config.example.toml", "-baseline", baselinePath,
	}, &stdout, &stderr); code != exitFailed {
		t.Fatalf("regressed run exit=%d, want %d (stdout=%s)", code, exitFailed, stdout.String())
	}
	if !strings.Contains(stdout.String(), "regressions=1") || !strings.Contains(stdout.String(), "cli-read-only") || !strings.Contains(stdout.String(), "regression") {
		t.Fatalf("regression was not reported with its case id: %s", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()

	// Approving a failing run as the new baseline must be refused.
	if code := run([]string{
		"-dataset", writeBrokenDataset(t), "-report", reportPath, "-code-version", "regressed",
		"-config", "../../config.example.toml", "-write-baseline", filepath.Join(dir, "bad-baseline.json"),
	}, &stdout, &stderr); code != exitSetup {
		t.Fatalf("approving a failing run exit=%d, want %d", code, exitSetup)
	}
	if !strings.Contains(stderr.String(), "refusing to write a baseline") {
		t.Fatalf("stderr=%s, want a refusal", stderr.String())
	}
}

func TestRunBaselineVersionMismatchFailsBeforeWritingAReport(t *testing.T) {
	dir := t.TempDir()
	datasetPath := writeTestDataset(t)
	reportPath := filepath.Join(dir, "report.json")

	mismatched := eval.NewBaseline("cli-contract", "cli-contract", "999", evaluator.Version, evaluator.ReportSchemaVersion, time.Unix(0, 0), []eval.CurrentVerdict{{CaseID: "cli-read-only", Passed: true}})
	mismatchedPath := filepath.Join(dir, "mismatched.json")
	if err := eval.WriteBaseline(mismatchedPath, mismatched); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{
		"-dataset", datasetPath, "-report", reportPath, "-code-version", "mismatch",
		"-config", "../../config.example.toml", "-baseline", mismatchedPath,
	}, &stdout, &stderr); code != exitSetup {
		t.Fatalf("version mismatch exit=%d, want %d", code, exitSetup)
	}
	if !strings.Contains(stderr.String(), "evaluation_setup_error") {
		t.Fatalf("stderr=%s, want evaluation_setup_error", stderr.String())
	}
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("an incomparable run still wrote a report: %v", err)
	}
}

// --- offline calibration replay (005) -------------------------------------

func fp(value float64) *float64 { return &value }

type calibrateFixture struct {
	materialsPath   string
	annotationsPath string
	predictionsPath string
	manifestPath    string
	manifest        semantic.RegisteredManifest
	materials       []semantic.Material
}

func calibrateTestManifest(t *testing.T, debug, holdout []string) semantic.RegisteredManifest {
	t.Helper()
	budget := semantic.BudgetIdentity{MaxCalls: 16, TimeoutMS: 5000, MaxInputBytes: semantic.MaxProjectionTotalBytes, MaxOutputBytes: 2048}
	budget.Digest = semantic.BudgetDigest(budget)
	scorer := semantic.ScorerIdentity{
		SchemaVersion: "1", Kind: semantic.ScorerFake, Provider: "local", Model: "fake-judge@1",
		ProviderRevisionStatus: semantic.ProviderRevisionUnverified,
		PromptVersion:          "judge-prompt@1", PromptDigest: semantic.DigestText("judge prompt"),
		Rubric: semantic.FrozenRubricIdentity(), Projection: semantic.FrozenProjectionIdentity(),
		MarkerSet:    semantic.FrozenSensitiveMarkerSetIdentity(),
		BudgetDigest: budget.Digest,
	}
	scorer.Digest = semantic.ScorerDigest(scorer)
	controls := make([]semantic.BiasControl, 0, 4)
	for _, name := range semantic.RequiredBiasControls() {
		controls = append(controls, semantic.BiasControl{Name: name, Verified: true})
	}
	manifest := semantic.RegisteredManifest{
		SchemaVersion: semantic.RegisteredManifestSchemaVersion, ManifestID: "cli-manifest",
		RegisteredAtUTC: time.Date(2026, 10, 9, 9, 0, 0, 0, time.UTC),
		Dimension:       semantic.DimensionAnswerRelevanceValue, Scorer: scorer, Budget: &budget,
		Splits: semantic.SplitPlan{
			SplitRule: semantic.SplitRuleByMaterialGroup, RepresentativeRule: semantic.RepresentativeRuleFirstEligibleBySampleID,
			DebugGroups: debug, HoldoutGroups: holdout,
		},
		Thresholds: semantic.Thresholds{
			MinIndependentTotal: 6,
			MinPerClass:         map[semantic.Label]int{semantic.LabelRelevant: 3, semantic.LabelIrrelevant: 3},
			MinAgreement:        fp(0.6), MinAgreementLowerBound: fp(0.3),
			MinKappa: fp(0.2), MinKappaLowerBound: fp(0.0),
			MinRecall:            map[semantic.Label]float64{semantic.LabelRelevant: 0.5, semantic.LabelIrrelevant: 0.5},
			MaxMissingRate:       fp(0.25),
			BiasControlTolerance: fp(0.1),
		},
		Bootstrap:    semantic.BootstrapRule{Resamples: 200, Seed: 7, Level: 0.95, MaxInvalidFraction: 0.25},
		BiasControls: controls,
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("fixture manifest: %v", err)
	}
	return manifest
}

// writeCalibrateFixture writes strict local material, annotation and candidate
// files plus a registered manifest, and returns their paths.
func writeCalibrateFixture(t *testing.T, mutate func(*calibrateFixture)) calibrateFixture {
	t.Helper()
	directory := t.TempDir()
	debug := []string{"g-debug-1", "g-debug-2"}
	holdout := []string{"g-holdout-1", "g-holdout-2", "g-holdout-3", "g-holdout-4", "g-holdout-5", "g-holdout-6"}
	manifest := calibrateTestManifest(t, debug, holdout)
	labels := []semantic.Label{
		semantic.LabelRelevant, semantic.LabelIrrelevant,
		semantic.LabelRelevant, semantic.LabelIrrelevant,
		semantic.LabelRelevant, semantic.LabelIrrelevant,
		semantic.LabelRelevant, semantic.LabelIrrelevant,
	}
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	materials := make([]semantic.Material, 0, 8)
	annotations := make([]semantic.Annotation, 0, 8)
	candidates := make([]semantic.CalibrationCandidate, 0, 8)
	for index, group := range append(append([]string{}, debug...), holdout...) {
		label := labels[index]
		spec := semantic.MaterialSpec{
			SchemaVersion: semantic.MaterialSchemaVersion,
			SampleID:      "sample-" + group, MaterialGroupID: group,
			DatasetName: "synthetic-operations", DatasetVersion: "5",
			CaseID: "positive-audience-query", CaseVersion: "1.0.0", VerdictDigest: "digest-" + group,
			Dimension: semantic.DimensionAnswerRelevanceValue, IntentSource: semantic.IntentSourceReviewedBusinessGoal,
			TaskIntent: "查询符合沉睡客户条件的合成客户数量", SourceApproval: semantic.SourceApprovalSynthetic,
			Applicability: semantic.ApplicabilityApplicable,
		}
		material := semantic.Material{
			MaterialSpec: spec, PublicAnswer: "符合条件的合成客户共 12 位",
			Rubric: semantic.FrozenRubricIdentity(), Projection: semantic.FrozenProjectionIdentity(),
		}
		material.MaterialDigest = semantic.MaterialDigest(material)
		if err := material.Validate(); err != nil {
			t.Fatalf("fixture material: %v", err)
		}
		materials = append(materials, material)
		annotations = append(annotations, semantic.Annotation{
			SchemaVersion: semantic.AnnotationSchemaVersion, AnnotationID: "annotation-" + group,
			SampleID: material.SampleID, MaterialDigest: material.MaterialDigest,
			Dimension: material.Dimension, Label: label, AnnotatorID: "annotator-1",
			AnnotatedAtUTC: at, Blind: true,
		})
		prediction := label
		candidates = append(candidates, semantic.CalibrationCandidate{
			SchemaVersion: semantic.CalibrationCandidateSchemaVersion, Purpose: semantic.CalibrationCandidatePurpose,
			ManifestDigest: manifest.Digest(), ScorerDigest: manifest.Scorer.Digest, ScorerKind: manifest.Scorer.Kind,
			SampleID: material.SampleID, MaterialGroupID: group, MaterialDigest: material.MaterialDigest,
			Dimension: material.Dimension, InvocationIndex: semantic.PrimaryInvocationIndex,
			Outcome: semantic.CandidatePrediction, Prediction: &prediction, RecordedAtUTC: at,
		})
	}

	fixture := calibrateFixture{
		materialsPath:   filepath.Join(directory, "materials.jsonl"),
		annotationsPath: filepath.Join(directory, "annotations.jsonl"),
		predictionsPath: filepath.Join(directory, "predictions.jsonl"),
		manifestPath:    filepath.Join(directory, "manifest.json"),
		manifest:        manifest, materials: materials,
	}
	if mutate != nil {
		mutate(&fixture)
	}
	if err := semantic.WriteJSONLNew(fixture.materialsPath, fixture.materials); err != nil {
		t.Fatal(err)
	}
	if err := semantic.WriteJSONLNew(fixture.annotationsPath, annotations); err != nil {
		t.Fatal(err)
	}
	if err := semantic.WriteJSONLNew(fixture.predictionsPath, candidates); err != nil {
		t.Fatal(err)
	}
	if err := semantic.WriteJSONSecret(fixture.manifestPath, fixture.manifest); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func calibrateArgs(fixture calibrateFixture, output string) []string {
	return []string{
		"-calibrate",
		"-semantic-materials", fixture.materialsPath,
		"-semantic-annotations", fixture.annotationsPath,
		"-calibration-predictions", fixture.predictionsPath,
		"-calibration-config", fixture.manifestPath,
		"-calibration-output", output,
	}
}

// The offline replay must consume only local files. The composition seam for the
// runtime runner is replaced with a failing stub, so any attempt to construct the
// runtime (and therefore any model or provider path) fails the test instead of
// being inferred from the absence of a network error.
func TestCalibrateReplaysLocalFilesWithoutConstructingTheRuntime(t *testing.T) {
	fixture := writeCalibrateFixture(t, nil)
	output := filepath.Join(t.TempDir(), "calibration.json")

	original := newCaseRunner
	newCaseRunner = func(string, string, bool) *runner.Runner {
		t.Fatal("-calibrate must not construct the runtime runner")
		return nil
	}
	defer func() { newCaseRunner = original }()

	var stdout, stderr bytes.Buffer
	// -config points at a path that does not exist: an offline replay does not
	// read model or credential configuration at all.
	code := run(append(calibrateArgs(fixture, output), "-config", filepath.Join(t.TempDir(), "missing.toml")), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status=usable") {
		t.Fatalf("expected a usable verdict, got %s", stdout.String())
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatalf("calibration report: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("calibration report mode: %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var report semantic.CalibrationReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Acceptance.Status != semantic.AcceptanceUsable {
		t.Fatalf("acceptance: %+v", report.Acceptance)
	}
	// The replay records the fake provenance it was computed from; it does not
	// upgrade a fake calibration into a real one.
	if report.Identity.ScorerKind != semantic.ScorerFake {
		t.Fatalf("scorer kind: %s", report.Identity.ScorerKind)
	}
	if report.Identity.SourceDigest == "" {
		t.Fatal("the identity must carry the allow-listed source revision")
	}
	if len(report.Splits) == 0 || report.HumanReliability.Status != semantic.EstimateNotEstimable {
		t.Fatalf("report must separate the statistics and the human reliability: %+v", report.HumanReliability)
	}
}

func TestCalibrateRejectsMixedModesAndMalformedInput(t *testing.T) {
	fixture := writeCalibrateFixture(t, nil)
	output := filepath.Join(t.TempDir(), "calibration.json")

	var stdout, stderr bytes.Buffer
	// A hard evaluation option must be refused rather than silently ignored.
	code := run(append(calibrateArgs(fixture, output), "-report", filepath.Join(t.TempDir(), "report.json")), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected exit 2 for a mixed mode, got %d", code)
	}
	if !strings.Contains(stderr.String(), "-calibrate") {
		t.Fatalf("expected an explicit mixed-mode error, got %s", stderr.String())
	}
	if _, err := os.Stat(output); err == nil {
		t.Fatal("a refused mode must not leave a calibration report behind")
	}

	// Missing required input is a setup error.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"-calibrate", "-semantic-materials", fixture.materialsPath}, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2 for missing inputs, got %d", code)
	}

	// Malformed registered rules are a setup error, not a semantic verdict.
	brokenManifest := filepath.Join(t.TempDir(), "broken-manifest.json")
	if err := os.WriteFile(brokenManifest, []byte(`{"schema_version":"1","unknown_field":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	args := calibrateArgs(fixture, output)
	for index := range args {
		if args[index] == fixture.manifestPath {
			args[index] = brokenManifest
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(args, &stdout, &stderr); code != 2 {
		t.Fatalf("expected exit 2 for a malformed manifest, got %d", code)
	}
	if _, err := os.Stat(output); err == nil {
		t.Fatal("a malformed manifest must not leave a calibration report behind")
	}
}

// A calibration that concludes "not usable" is a successful command: a semantic
// conclusion must never be reported as a hard evaluation failure.
func TestCalibrateUnusableVerdictIsStillASuccessfulCommand(t *testing.T) {
	fixture := writeCalibrateFixture(t, nil)
	// Overwrite the candidates with a constant prediction for every material.
	at := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	relevant := semantic.LabelRelevant
	candidates := make([]semantic.CalibrationCandidate, 0, len(fixture.materials))
	for _, material := range fixture.materials {
		candidates = append(candidates, semantic.CalibrationCandidate{
			SchemaVersion: semantic.CalibrationCandidateSchemaVersion, Purpose: semantic.CalibrationCandidatePurpose,
			ManifestDigest: fixture.manifest.Digest(), ScorerDigest: fixture.manifest.Scorer.Digest,
			ScorerKind: fixture.manifest.Scorer.Kind, SampleID: material.SampleID,
			MaterialGroupID: material.MaterialGroupID, MaterialDigest: material.MaterialDigest,
			Dimension: material.Dimension, InvocationIndex: semantic.PrimaryInvocationIndex,
			Outcome: semantic.CandidatePrediction, Prediction: &relevant, RecordedAtUTC: at,
		})
	}
	predictions := filepath.Join(t.TempDir(), "constant-predictions.jsonl")
	if err := semantic.WriteJSONLNew(predictions, candidates); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "calibration.json")
	args := calibrateArgs(fixture, output)
	for index := range args {
		if args[index] == fixture.predictionsPath {
			args[index] = predictions
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("an unusable calibration is not a command failure: exit=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "not usable") {
		t.Fatalf("expected the verdict to be stated, got %s", stdout.String())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var report semantic.CalibrationReport
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Acceptance.Status == semantic.AcceptanceUsable {
		t.Fatal("a constant scorer must not be reported as usable")
	}
}

// A schema-3 run must refuse a schema-2 baseline before executing any Case: the
// two envelopes describe different measurements, and a comparison across them
// would not be a regression signal.
func TestRunRefusesSchemaTwoBaselineBeforeWritingAReport(t *testing.T) {
	directory := t.TempDir()
	reportPath := filepath.Join(directory, "report.json")
	baselinePath := filepath.Join(directory, "baseline-v2.json")
	stale := eval.NewBaseline("cli-contract", "cli-contract", "1", evaluator.Version, 2,
		time.Unix(0, 0).UTC(), []eval.CurrentVerdict{{CaseID: "cli-read-only", CaseVersion: "1", Passed: true}})
	if err := eval.WriteBaseline(baselinePath, stale); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-semantic-judge", "off",
		"-dataset", writeTestDataset(t), "-report", reportPath,
		"-code-version", "migration", "-config", "../../config.example.toml",
		"-baseline", baselinePath,
	}, &stdout, &stderr)
	if code != exitSetup {
		t.Fatalf("v3 against a v2 baseline: exit=%d, want %d (stderr=%s)", code, exitSetup, stderr.String())
	}
	if !strings.Contains(stderr.String(), "report_schema_version") {
		t.Fatalf("the refusal must name the version mismatch: %s", stderr.String())
	}
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("a refused run must leave no report artifact: %v", err)
	}
}

// --- candidate sampling and the formal zero-call guarantee (005 FR-014/SC-007)

// samplingFixture approves the fixture materials and registers the finite budget,
// which is what authorizes candidate collection.
func samplingFixture(t *testing.T, fixture calibrateFixture) (manifestPath, budgetPath string) {
	t.Helper()
	directory := t.TempDir()
	manifest := fixture.manifest
	budget := *manifest.Budget
	budgetPath = filepath.Join(directory, "budget.json")
	if err := semantic.WriteJSONSecret(budgetPath, budget); err != nil {
		t.Fatal(err)
	}
	for _, material := range fixture.materials {
		manifest.ApprovedMaterials = append(manifest.ApprovedMaterials, semantic.ApprovedMaterial{
			SampleID: material.SampleID, MaterialGroupID: material.MaterialGroupID,
			MaterialDigest: material.MaterialDigest,
		})
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("sampling manifest: %v", err)
	}
	manifestPath = filepath.Join(directory, "sampling-manifest.json")
	if err := semantic.WriteJSONSecret(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	return manifestPath, budgetPath
}

func samplingArgs(fixture calibrateFixture, manifestPath, budgetPath, predictions string) []string {
	return []string{
		"-calibration-sample", "-semantic-judge", "fake",
		"-semantic-materials", fixture.materialsPath,
		"-calibration-config", manifestPath,
		"-semantic-budget", budgetPath,
		"-calibration-predictions", predictions,
	}
}

// The closed loop: uncalibrated candidate collection writes an evidence file,
// and the offline replay consumes it without any model call.
func TestCalibrationSampleThenOfflineCalibrationClosesTheLoop(t *testing.T) {
	fixture := writeCalibrateFixture(t, nil)
	manifestPath, budgetPath := samplingFixture(t, fixture)
	predictions := filepath.Join(t.TempDir(), "candidates.jsonl")

	original := newCaseRunner
	newCaseRunner = func(string, string, bool) *runner.Runner {
		t.Fatal("-calibration-sample must not construct the runtime runner")
		return nil
	}
	defer func() { newCaseRunner = original }()

	var stdout, stderr bytes.Buffer
	if code := run(samplingArgs(fixture, manifestPath, budgetPath, predictions), &stdout, &stderr); code != 0 {
		t.Fatalf("sampling exit=%d stderr=%s", code, stderr.String())
	}
	info, err := os.Stat(predictions)
	if err != nil {
		t.Fatalf("candidate file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("candidate file mode=%o, want 0600", info.Mode().Perm())
	}
	candidates, err := semantic.LoadCalibrationCandidates(predictions)
	if err != nil {
		t.Fatalf("sampled candidates must load: %v", err)
	}
	if len(candidates) != len(fixture.materials) {
		t.Fatalf("every planned invocation needs a record: got %d, want %d", len(candidates), len(fixture.materials))
	}
	for _, candidate := range candidates {
		if candidate.Purpose != semantic.CalibrationCandidatePurpose || candidate.Outcome != semantic.CandidatePrediction {
			t.Fatalf("unexpected candidate: %+v", candidate)
		}
	}
	// A collection must not overwrite an existing evidence file.
	stdout.Reset()
	stderr.Reset()
	if code := run(samplingArgs(fixture, manifestPath, budgetPath, predictions), &stdout, &stderr); code != exitSetup {
		t.Fatalf("overwriting candidate evidence must fail: exit=%d", code)
	}

	// The offline replay reads the candidates and an independently labelled
	// annotation file; it never calls a model.
	calibrationPath := filepath.Join(t.TempDir(), "calibration.json")
	stdout.Reset()
	stderr.Reset()
	code := run([]string{
		"-calibrate",
		"-semantic-materials", fixture.materialsPath,
		"-semantic-annotations", fixture.annotationsPath,
		"-calibration-predictions", predictions,
		"-calibration-config", manifestPath,
		"-calibration-output", calibrationPath,
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("replay exit=%d stderr=%s", code, stderr.String())
	}
	report, err := semantic.LoadCalibrationReport(calibrationPath)
	if err != nil {
		t.Fatalf("calibration report: %v", err)
	}
	// The fake answers every material "relevant", which is a constant prediction:
	// the replay must report the real numbers and refuse to accept the scorer.
	if report.Acceptance.Status == semantic.AcceptanceUsable {
		t.Fatal("a constant scorer must not become usable")
	}
	if !strings.Contains(stdout.String(), "not usable") {
		t.Fatalf("the verdict must be stated: %s", stdout.String())
	}
	if report.Scorer.Kind != semantic.ScorerFake {
		t.Fatalf("the candidate provenance must be preserved: %s", report.Scorer.Kind)
	}
}

// Sampling must refuse before any call when the mode, the manifest or the
// approved material set does not line up, and must not leave a candidate file.
func TestCalibrationSampleRejectsInvalidSetupsBeforeAnyCall(t *testing.T) {
	fixture := writeCalibrateFixture(t, nil)
	manifestPath, budgetPath := samplingFixture(t, fixture)

	cases := map[string]func(string) []string{
		"mixed with a hard report": func(predictions string) []string {
			return append(samplingArgs(fixture, manifestPath, budgetPath, predictions), "-report", filepath.Join(t.TempDir(), "r.json"))
		},
		"loads human labels": func(predictions string) []string {
			return append(samplingArgs(fixture, manifestPath, budgetPath, predictions), "-semantic-annotations", fixture.annotationsPath)
		},
		"judge off": func(predictions string) []string {
			args := samplingArgs(fixture, manifestPath, budgetPath, predictions)
			for index := range args {
				if args[index] == "fake" {
					args[index] = "off"
				}
			}
			return args
		},
		"missing budget": func(predictions string) []string {
			return []string{
				"-calibration-sample", "-semantic-judge", "fake",
				"-semantic-materials", fixture.materialsPath,
				"-calibration-config", manifestPath,
				"-calibration-predictions", predictions,
			}
		},
		"unregistered material group": func(predictions string) []string {
			// Move a group out of the registered plan, keeping everything else.
			relocated := calibrateTestManifest(t, []string{"g-debug-1"}, []string{"g-holdout-1", "g-holdout-2", "g-holdout-3", "g-holdout-4", "g-holdout-5"})
			budget := *relocated.Budget
			path := filepath.Join(t.TempDir(), "relocated.json")
			for _, material := range fixture.materials {
				relocated.ApprovedMaterials = append(relocated.ApprovedMaterials, semantic.ApprovedMaterial{
					SampleID: material.SampleID, MaterialGroupID: material.MaterialGroupID, MaterialDigest: material.MaterialDigest,
				})
			}
			_ = budget
			if err := semantic.WriteJSONSecret(path, relocated); err != nil {
				t.Fatal(err)
			}
			args := samplingArgs(fixture, path, budgetPath, predictions)
			return args
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			predictions := filepath.Join(t.TempDir(), "candidates.jsonl")
			var stdout, stderr bytes.Buffer
			code := run(build(predictions), &stdout, &stderr)
			if code != exitSetup {
				t.Fatalf("expected exit %d, got %d (stderr=%s)", exitSetup, code, stderr.String())
			}
			if _, err := os.Stat(predictions); !os.IsNotExist(err) {
				t.Fatalf("a refused collection must not leave candidate evidence: %v", err)
			}
		})
	}
}

// Contaminated material never reaches the provider: it is recorded as a closed
// error so it stays in the denominator, and the outbound request count is zero.
func TestCalibrationSampleRecordsPrivacyFailuresWithoutCallingOut(t *testing.T) {
	fixture := writeCalibrateFixture(t, func(f *calibrateFixture) {
		contaminated := f.materials[0]
		contaminated.PublicAnswer = "答复 " + semantic.SensitiveCanaryMarker
		contaminated.MaterialDigest = semantic.MaterialDigest(contaminated)
		f.materials[0] = contaminated
	})
	manifestPath, budgetPath := samplingFixture(t, fixture)
	predictions := filepath.Join(t.TempDir(), "candidates.jsonl")

	var stdout, stderr bytes.Buffer
	if code := run(samplingArgs(fixture, manifestPath, budgetPath, predictions), &stdout, &stderr); code != 0 {
		t.Fatalf("collection exit=%d stderr=%s", code, stderr.String())
	}
	candidates, err := semantic.LoadCalibrationCandidates(predictions)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != len(fixture.materials) {
		t.Fatalf("a refused material must stay in the denominator: got %d, want %d", len(candidates), len(fixture.materials))
	}
	refused := 0
	for _, candidate := range candidates {
		if candidate.Outcome == semantic.CandidateError && candidate.ReasonCode == semantic.ReasonSensitiveInput {
			refused++
		}
	}
	if refused != 1 {
		t.Fatalf("expected exactly one closed privacy refusal, got %d", refused)
	}
	if strings.Contains(stdout.String()+stderr.String(), semantic.SensitiveCanaryMarker) {
		t.Fatal("the planted marker must never be echoed back")
	}
}

// Formal scoring has no skip-calibration switch: without an identity-matched
// usable calibration a real scorer is never even constructed, so the provider
// sees zero requests while the hard evaluation continues unchanged.
func TestFormalRealJudgeMakesZeroRequestsWithoutUsableCalibration(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"{\"label\":\"relevant\",\"reason_code\":\"addresses_intent\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	// A local stand-in for the provider, wired through the real configuration
	// path so no network call can escape unnoticed.
	base, err := os.ReadFile("../../config.example.toml")
	if err != nil {
		t.Fatal(err)
	}
	config := strings.Replace(string(base), `base_url = "https://api.deepseek.com"`, `base_url = "`+server.URL+`"`, 1)
	config = strings.Replace(config, `api_key_env = "LLM_API_KEY"`, `api_key_env = "TEST_JUDGE_REAL_KEY"`, 1)
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_JUDGE_REAL_KEY", "test-credential-value")

	fixture := writeCalibrateFixture(t, func(f *calibrateFixture) {
		replaced := make([]semantic.Material, 0, len(f.materials))
		for _, material := range f.materials {
			material.CaseID = "cli-read-only"
			material.CaseVersion = "1"
			material.MaterialDigest = semantic.MaterialDigest(material)
			replaced = append(replaced, material)
		}
		f.materials = replaced
	})
	_, budgetPath := samplingFixture(t, fixture)
	reportPath := filepath.Join(t.TempDir(), "report.json")

	var stdout, stderr bytes.Buffer
	code := run([]string{
		"-semantic-judge", "real",
		"-semantic-materials", fixture.materialsPath,
		"-semantic-budget", budgetPath,
		"-dataset", writeTestDataset(t), "-report", reportPath,
		"-code-version", "zero-call", "-config", configPath,
	}, &stdout, &stderr)
	if code != 0 {
		// The hard evaluation must not be disturbed by the semantic layer.
		t.Fatalf("hard evaluation exit=%d stderr=%s", code, stderr.String())
	}
	if requests != 0 {
		t.Fatalf("an uncalibrated formal call must make zero requests, got %d", requests)
	}
	report := readReport(t, reportPath)
	if report.Passed != 1 || report.Failed != 0 {
		t.Fatalf("hard verdicts must be untouched: passed=%d failed=%d", report.Passed, report.Failed)
	}
	entry := report.Cases[0].Semantic
	if entry == nil {
		t.Fatal("every schema-3 Case carries its semantic state")
	}
	if entry.Label != nil {
		t.Fatalf("an uncalibrated run must not produce a formal label: %+v", entry)
	}
	if entry.ExecutionStatus != semantic.ExecutionUnverified || entry.AcceptanceStatus != semantic.AcceptanceNotCalibrated {
		t.Fatalf("unexpected semantic state: %+v", entry)
	}
	if entry.ReasonCode != semantic.ReasonNotCalibrated {
		t.Fatalf("expected not_calibrated, got %q", entry.ReasonCode)
	}
}
