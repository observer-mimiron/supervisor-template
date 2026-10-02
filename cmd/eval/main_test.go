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
