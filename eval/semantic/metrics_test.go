package semantic

import (
	"math"
	"testing"
)

func almostEqual(t *testing.T, got, want, tolerance float64, label string) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Fatalf("%s must be a finite number, got %v", label, got)
	}
	if math.Abs(got-want) > tolerance {
		t.Fatalf("%s: got %v, want %v (±%v)", label, got, want, tolerance)
	}
}

func mustValue(t *testing.T, estimate Estimate, label string) float64 {
	t.Helper()
	if estimate.Status != EstimateEstimated || estimate.Value == nil {
		t.Fatalf("%s: expected an estimated value, got status=%s reason=%s", label, estimate.Status, estimate.Reason)
	}
	if math.IsNaN(*estimate.Value) || math.IsInf(*estimate.Value, 0) {
		t.Fatalf("%s: NaN/Inf is never a measurement (%v)", label, *estimate.Value)
	}
	return *estimate.Value
}

func matrixFrom(t *testing.T, pairs []LabelPair) ConfusionMatrix {
	t.Helper()
	matrix := NewConfusionMatrix()
	for _, pair := range pairs {
		if err := matrix.Add(pair.Human, pair.Judge); err != nil {
			t.Fatalf("add pair: %v", err)
		}
	}
	return matrix
}

func TestConfusionMatrixCountsRecallAndAgreement(t *testing.T) {
	// Human rows: relevant x4 (2->relevant, 1->irrelevant, 1->not_applicable),
	// irrelevant x2 (0->relevant, 2->irrelevant).
	matrix := matrixFrom(t, []LabelPair{
		{LabelRelevant, LabelRelevant}, {LabelRelevant, LabelRelevant},
		{LabelRelevant, LabelIrrelevant}, {LabelRelevant, LabelNotApplicable},
		{LabelIrrelevant, LabelIrrelevant}, {LabelIrrelevant, LabelIrrelevant},
	})
	if matrix.Total() != 6 {
		t.Fatalf("total: %d", matrix.Total())
	}
	if matrix.HumanCount(LabelRelevant) != 4 || matrix.HumanCount(LabelIrrelevant) != 2 {
		t.Fatalf("human counts wrong: %+v", matrix.Counts)
	}
	if matrix.PredictionCount(LabelNotApplicable) != 1 {
		t.Fatalf("prediction counts wrong: %+v", matrix.Counts)
	}
	// Recall(relevant) = 2/4, recall(irrelevant) = 2/2.
	almostEqual(t, mustValue(t, matrix.Recall(LabelRelevant), "recall(relevant)"), 0.5, 1e-9, "recall(relevant)")
	almostEqual(t, mustValue(t, matrix.Recall(LabelIrrelevant), "recall(irrelevant)"), 1.0, 1e-9, "recall(irrelevant)")
	// A class with no human labels has no denominator.
	empty := matrix.Recall(LabelNotApplicable)
	if empty.Status != EstimateNotEstimable || empty.Reason != NotEstimableEmptyClassDenominator {
		t.Fatalf("expected not_estimable for an empty class denominator, got %+v", empty)
	}
	// Agreement = 4/6.
	almostEqual(t, mustValue(t, matrix.Agreement(), "agreement"), 4.0/6.0, 1e-9, "agreement")
	// A Judge mispredicting not_applicable stays visible as a disagreement in
	// the applicable statistics instead of being silently dropped.
	if matrix.Counts[labelIndex(LabelRelevant)][labelIndex(LabelNotApplicable)] != 1 {
		t.Fatal("a not_applicable prediction must remain a confusion-matrix entry")
	}
}

// The counterexample the specification names explicitly: balanced human labels
// against a constant prediction. Both numbers are real, and neither is a pass.
func TestBalancedLabelsAgainstConstantPredictionKeepRealValues(t *testing.T) {
	matrix := matrixFrom(t, []LabelPair{
		{LabelRelevant, LabelRelevant},
		{LabelIrrelevant, LabelRelevant},
	})
	agreement := mustValue(t, matrix.Agreement(), "agreement")
	almostEqual(t, agreement, 0.5, 1e-9, "agreement")
	kappa := mustValue(t, matrix.Kappa(), "kappa")
	almostEqual(t, kappa, 0.0, 1e-9, "kappa")
	// The scorer is unusable, and that is recorded separately from the numbers.
	groups := []GroupPairs{{GroupID: "g1", Pairs: []LabelPair{{LabelRelevant, LabelRelevant}, {LabelIrrelevant, LabelRelevant}}}}
	metrics, err := ComputePairMetrics(groups, BootstrapRule{Resamples: 50, Seed: 3, Level: 0.95, MaxInvalidFraction: 1})
	if err != nil {
		t.Fatal(err)
	}
	if name, degenerate := metrics.IsDegenerate(); !degenerate || name != "constant_prediction" {
		t.Fatalf("a constant prediction must be flagged degenerate, got %q degenerate=%t", name, degenerate)
	}
}

func TestSingleClassHumanLabelsAreDegenerateButKeepDefinedMetrics(t *testing.T) {
	matrix := matrixFrom(t, []LabelPair{
		{LabelRelevant, LabelRelevant}, {LabelRelevant, LabelRelevant},
	})
	// All-relevant on both sides: the expected agreement is 1, so kappa has no
	// denominator -- and that is reported, not faked as 1.
	almostEqual(t, mustValue(t, matrix.Agreement(), "agreement"), 1.0, 1e-9, "agreement")
	almostEqual(t, mustValue(t, matrix.ExpectedAgreement(), "expected"), 1.0, 1e-9, "expected agreement")
	kappa := matrix.Kappa()
	if kappa.Status != EstimateNotEstimable || kappa.Reason != NotEstimableZeroExpectedAgreement {
		t.Fatalf("expected zero_expected_agreement, got %+v", kappa)
	}
	groups := []GroupPairs{{GroupID: "g1", Pairs: []LabelPair{{LabelRelevant, LabelRelevant}, {LabelRelevant, LabelRelevant}}}}
	metrics, err := ComputePairMetrics(groups, BootstrapRule{Resamples: 20, Seed: 1, Level: 0.95, MaxInvalidFraction: 1})
	if err != nil {
		t.Fatal(err)
	}
	if name, degenerate := metrics.IsDegenerate(); !degenerate || name != "single_class_human_labels" {
		t.Fatalf("single-class labels must be flagged degenerate, got %q", name)
	}
	if metrics.DistinctHumanLabels != 1 {
		t.Fatalf("distinct human labels: %d", metrics.DistinctHumanLabels)
	}
}

func TestNoPairsIsNotEstimableAndNeverNaN(t *testing.T) {
	matrix := NewConfusionMatrix()
	agreement := matrix.Agreement()
	if agreement.Status != EstimateNotEstimable || agreement.Reason != NotEstimableNoPairs || agreement.Value != nil {
		t.Fatalf("expected value=null/not_estimable/no_pairs, got %+v", agreement)
	}
	kappa := matrix.Kappa()
	if kappa.Status != EstimateNotEstimable || kappa.Reason != NotEstimableNoPairs {
		t.Fatalf("expected not_estimable kappa, got %+v", kappa)
	}
	metrics, err := ComputePairMetrics(nil, BootstrapRule{Resamples: 10, Seed: 1, Level: 0.95})
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Pairs != 0 || metrics.KappaCI.Status != EstimateNotEstimable {
		t.Fatalf("an empty set has no interval: %+v", metrics.KappaCI)
	}
	if _, degenerate := metrics.IsDegenerate(); !degenerate {
		t.Fatal("an empty set is degenerate")
	}
}

func TestWilsonIntervalGoldenValues(t *testing.T) {
	// z_(0.975) = 1.959963984540054. For 5/10 the interval is symmetric around
	// 0.5 with half-width z/(1+z^2/n) * sqrt(p(1-p)/n + z^2/(4n^2)).
	// The inverse CDF is Acklam's rational approximation, whose documented
	// accuracy is ~1.15e-9 relative; the tolerance reflects that instead of
	// pretending it is exact.
	almostEqual(t, normalQuantile(0.975), 1.959963984540054, 1e-8, "z(0.975)")
	// Symmetry of the standard normal quantile function.
	almostEqual(t, normalQuantile(0.025), -normalQuantile(0.975), 1e-8, "z(0.025)")
	almostEqual(t, normalQuantile(0.5), 0, 1e-12, "z(0.5)")
	interval, err := Wilson(5, 10, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	almostEqual(t, interval.Low, 0.236593090512564, 1e-9, "wilson low")
	almostEqual(t, interval.High, 0.763406909487436, 1e-9, "wilson high")
	almostEqual(t, interval.Level, 0.95, 1e-12, "wilson level")

	// Degenerate proportions stay inside [0,1] and never leave a NaN.
	none, err := Wilson(0, 10, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	almostEqual(t, none.Low, 0.0, 1e-12, "wilson low at 0/10")
	almostEqual(t, none.High, 0.277532799862889, 1e-9, "wilson high at 0/10")
	all, err := Wilson(10, 10, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	almostEqual(t, all.Low, 0.722467200137111, 1e-9, "wilson low at 10/10")
	almostEqual(t, all.High, 1.0, 1e-12, "wilson high at 10/10")

	if _, err := Wilson(0, 0, 0.95); err == nil {
		t.Fatal("an empty denominator must be refused, not returned as 0..1")
	}
	if _, err := Wilson(3, 2, 0.95); err == nil {
		t.Fatal("more successes than trials must be refused")
	}
}

func TestGroupBootstrapIsDeterministicAndGroupLevel(t *testing.T) {
	groups := []GroupPairs{
		{GroupID: "g1", Pairs: []LabelPair{{LabelRelevant, LabelRelevant}, {LabelIrrelevant, LabelIrrelevant}}},
		{GroupID: "g2", Pairs: []LabelPair{{LabelRelevant, LabelIrrelevant}, {LabelIrrelevant, LabelRelevant}}},
		{GroupID: "g3", Pairs: []LabelPair{{LabelRelevant, LabelRelevant}, {LabelIrrelevant, LabelRelevant}}},
	}
	rule := BootstrapRule{Resamples: 300, Seed: 42, Level: 0.95, MaxInvalidFraction: 0.5}
	first := GroupBootstrapKappa(groups, rule)
	second := GroupBootstrapKappa(groups, rule)
	if first.Status != EstimateEstimated || first.Interval == nil {
		t.Fatalf("expected an estimated interval, got %+v", first)
	}
	if first.Interval.Low != second.Interval.Low || first.Interval.High != second.Interval.High {
		t.Fatalf("the same seed must reproduce the interval: %+v vs %+v", first.Interval, second.Interval)
	}
	if first.Effective+first.Invalid != rule.Resamples {
		t.Fatalf("every resample must be accounted for: effective=%d invalid=%d", first.Effective, first.Invalid)
	}
	if first.Seed != rule.Seed || first.Resamples != rule.Resamples {
		t.Fatalf("the rule must be reported: %+v", first)
	}
	if first.Interval.Low > first.Interval.High {
		t.Fatalf("interval ordering: %+v", first.Interval)
	}

	// A different seed is a different draw but the same measurement; it must
	// still produce a usable interval rather than being assumed identical.
	other := GroupBootstrapKappa(groups, BootstrapRule{Resamples: 300, Seed: 43, Level: 0.95, MaxInvalidFraction: 0.5})
	if other.Status != EstimateEstimated {
		t.Fatalf("a different seed must still produce an interval: %+v", other)
	}
}

// Degenerate draws are refused, never silently dropped to manufacture a green
// result.
func TestGroupBootstrapRefusesWhenTooManyDrawsAreDegenerate(t *testing.T) {
	// Every group is single-class on both sides, so kappa has no denominator in
	// any resample.
	groups := []GroupPairs{
		{GroupID: "g1", Pairs: []LabelPair{{LabelRelevant, LabelRelevant}}},
		{GroupID: "g2", Pairs: []LabelPair{{LabelRelevant, LabelRelevant}}},
	}
	strict := GroupBootstrapKappa(groups, BootstrapRule{Resamples: 100, Seed: 5, Level: 0.95, MaxInvalidFraction: 0})
	if strict.Status != EstimateNotEstimable || strict.Reason != NotEstimableNoValidBootstrapResample {
		t.Fatalf("expected a refused interval, got %+v", strict)
	}
	if strict.Interval != nil {
		t.Fatal("a refused interval must not carry bounds")
	}
	if strict.Invalid != 100 || strict.Effective != 0 {
		t.Fatalf("counts must show why: %+v", strict)
	}
}

func TestPercentileInterpolation(t *testing.T) {
	values := []float64{1, 2, 3, 4, 5}
	almostEqual(t, percentile(values, 0), 1, 1e-12, "p0")
	almostEqual(t, percentile(values, 1), 5, 1e-12, "p100")
	almostEqual(t, percentile(values, 0.5), 3, 1e-12, "p50")
	almostEqual(t, percentile(values, 0.25), 2, 1e-12, "p25")
	almostEqual(t, percentile(values, 0.125), 1.5, 1e-12, "p12.5")
}

func TestPairMetricsNeverContainNaNOrInfinity(t *testing.T) {
	groups := []GroupPairs{
		{GroupID: "g1", Pairs: []LabelPair{{LabelRelevant, LabelIrrelevant}, {LabelIrrelevant, LabelRelevant}}},
		{GroupID: "g2", Pairs: []LabelPair{{LabelRelevant, LabelRelevant}, {LabelIrrelevant, LabelIrrelevant}}},
		{GroupID: "g3", Pairs: []LabelPair{{LabelNotApplicable, LabelNotApplicable}}},
	}
	metrics, err := ComputePairMetrics(groups, BootstrapRule{Resamples: 120, Seed: 11, Level: 0.95, MaxInvalidFraction: 0.5})
	if err != nil {
		t.Fatal(err)
	}
	estimates := map[string]Estimate{"agreement": metrics.Agreement, "expected": metrics.ExpectedAgreement, "kappa": metrics.Kappa}
	for _, label := range AllLabels() {
		estimates["recall:"+string(label)] = metrics.Recall[label]
	}
	for name, estimate := range estimates {
		if estimate.Value == nil {
			continue
		}
		if math.IsNaN(*estimate.Value) || math.IsInf(*estimate.Value, 0) {
			t.Fatalf("%s is not a number: %v", name, *estimate.Value)
		}
	}
	if metrics.AgreementCI != nil {
		almostEqual(t, metrics.AgreementCI.Level, 0.95, 1e-12, "agreement interval level")
	}
	if metrics.KappaCI.Interval != nil && metrics.KappaCI.Interval.Low > metrics.KappaCI.Interval.High {
		t.Fatalf("bootstrap interval ordering: %+v", metrics.KappaCI.Interval)
	}
	if metrics.Pairs != 5 || metrics.Groups != 3 {
		t.Fatalf("counts: pairs=%d groups=%d", metrics.Pairs, metrics.Groups)
	}
}
