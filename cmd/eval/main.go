package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	eval "github.com/observer-mimiron/supervisor-template/eval"
	"github.com/observer-mimiron/supervisor-template/eval/evaluator"
	"github.com/observer-mimiron/supervisor-template/eval/runner"
)

func main() {
	datasetPath := flag.String("dataset", "./eval/datasets/synthetic-operations-v1.json", "dataset path")
	reportPath := flag.String("report", "./tmp/eval-report.json", "report path")
	jsonlPath := flag.String("jsonl", "", "optional JSONL report path")
	codeVersion := flag.String("code-version", "dev", "code version")
	configPath := flag.String("config", "./config.example.toml", "server config path")
	riskLevel := flag.String("risk", os.Getenv("EVAL_RISK_LEVEL"), "declared PR risk: low, medium, high, or unknown")
	impactTags := flag.String("impact-tags", os.Getenv("EVAL_IMPACT_TAGS"), "comma-separated impact tags")
	flag.Parse()
	dataset, err := eval.Load(*datasetPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	selection := eval.Selection{RiskLevel: *riskLevel, ImpactTags: eval.NormalizeImpactTags(*impactTags)}
	selectedCases, err := eval.SelectCases(dataset, selection)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := os.MkdirAll(filepath.Dir(*reportPath), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	run := runner.New(*configPath, *codeVersion, runner.DefaultToken)
	cases := make([]evaluator.CaseReport, 0, len(selectedCases))
	for _, item := range selectedCases {
		evidence, runErr := run.RunCase(context.Background(), item)
		if runErr != nil {
			evidence.Error = runErr.Error()
		}
		cases = append(cases, evaluator.Evaluate(item, evidence))
	}
	report := evaluator.BuildWithSelection(dataset, *codeVersion, selection, cases)
	if err := evaluator.WriteJSON(*reportPath, report); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *jsonlPath != "" {
		if err := evaluator.WriteJSONL(*jsonlPath, report); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	fmt.Printf("evaluation dataset=%s@%s cases=%d passed=%d failed=%d\n", report.DatasetName, report.DatasetVersion, len(report.Cases), report.Passed, report.Failed)
	for _, item := range report.Cases {
		status := "PASS"
		if !item.Passed {
			status = "FAIL"
		}
		fmt.Printf("  %-32s %s terminal=%s writes=%d\n", item.CaseID, status, item.Evidence.Terminal, item.Evidence.FakeWriteCount)
	}
	if report.Failed > 0 {
		os.Exit(1)
	}
}
