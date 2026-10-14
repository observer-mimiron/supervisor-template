package semantic

import (
	"math"
	"math/rand"
	"sort"
)

// Statistics for one frozen dimension, computed with the Go standard library
// only. A single agreement percentage would let a majority-class or
// duplicate-material scorer look good, so the report always carries the full 3x3
// matrix, per-class recall, agreement with an interval, and kappa with a
// group-level interval.
//
// Two rules keep the numbers honest:
//
//   - An undefined denominator or a failed computation is reported as
//     value=null, status=not_estimable and a closed reason. Never NaN, never a
//     silent 0 or 1 that reads as a real measurement.
//   - A defined metric stays numeric even when the scorer is unusable. Balanced
//     human labels against a constant prediction have agreement 0.5 and kappa 0;
//     those are true values, and the *unusability* is a policy decision recorded
//     separately.

// EstimateStatus is the closed "did this metric have a valid denominator" state.
type EstimateStatus string

// Closed estimate statuses.
const (
	EstimateEstimated    EstimateStatus = "estimated"
	EstimateNotEstimable EstimateStatus = "not_estimable"
)

// NotEstimableReason is a closed explanation for a missing number.
type NotEstimableReason string

// Closed not-estimable reasons.
const (
	NotEstimableNoPairs                  NotEstimableReason = "no_pairs"
	NotEstimableZeroExpectedAgreement    NotEstimableReason = "zero_expected_agreement"
	NotEstimableEmptyClassDenominator    NotEstimableReason = "empty_class_denominator"
	NotEstimableEmptyDenominator         NotEstimableReason = "empty_denominator"
	NotEstimableNoValidBootstrapResample NotEstimableReason = "no_valid_bootstrap_resample"
)

// DefaultIntervalLevel is the registered confidence level (95%).
const DefaultIntervalLevel = 0.95

// Estimate is a metric that may be undefined.
type Estimate struct {
	Value  *float64           `json:"value"`
	Status EstimateStatus     `json:"status"`
	Reason NotEstimableReason `json:"reason,omitempty"`
	N      int                `json:"n"`
}

func estimated(value float64, n int) Estimate {
	return Estimate{Value: &value, Status: EstimateEstimated, N: n}
}

func notEstimable(reason NotEstimableReason, n int) Estimate {
	return Estimate{Status: EstimateNotEstimable, Reason: reason, N: n}
}

// Interval is a bounded confidence interval.
type Interval struct {
	Low   float64 `json:"low"`
	High  float64 `json:"high"`
	Level float64 `json:"level"`
}

// LabelPair is one paired human label and Judge prediction.
type LabelPair struct {
	Human Label `json:"human"`
	Judge Label `json:"judge"`
}

// ConfusionMatrix is the fixed 3x3 table: rows are human labels, columns are
// Judge labels.
type ConfusionMatrix struct {
	Labels []Label `json:"labels"`
	Counts [][]int `json:"counts"`
}

// NewConfusionMatrix builds an empty matrix over the frozen label set.
func NewConfusionMatrix() ConfusionMatrix {
	labels := AllLabels()
	counts := make([][]int, len(labels))
	for index := range counts {
		counts[index] = make([]int, len(labels))
	}
	return ConfusionMatrix{Labels: labels, Counts: counts}
}

func labelIndex(label Label) int {
	for index, candidate := range AllLabels() {
		if candidate == label {
			return index
		}
	}
	return -1
}

// Add records one pair. Unknown labels are refused rather than bucketed into an
// "other" column, which would silently change the denominator.
func (m *ConfusionMatrix) Add(human, judge Label) error {
	row, column := labelIndex(human), labelIndex(judge)
	if row < 0 || column < 0 {
		return Reject(ReasonInvalidOutput, "confusion matrix only accepts the frozen labels")
	}
	m.Counts[row][column]++
	return nil
}

// Total is the number of paired observations.
func (m ConfusionMatrix) Total() int {
	total := 0
	for _, row := range m.Counts {
		for _, value := range row {
			total += value
		}
	}
	return total
}

// HumanCount is the number of human labels in one class.
func (m ConfusionMatrix) HumanCount(label Label) int {
	row := labelIndex(label)
	if row < 0 {
		return 0
	}
	total := 0
	for _, value := range m.Counts[row] {
		total += value
	}
	return total
}

// PredictionCount is the number of Judge predictions of one class.
func (m ConfusionMatrix) PredictionCount(label Label) int {
	column := labelIndex(label)
	if column < 0 {
		return 0
	}
	total := 0
	for _, row := range m.Counts {
		total += row[column]
	}
	return total
}

// Agreement is the raw observed agreement across all pairs.
func (m ConfusionMatrix) Agreement() Estimate {
	total := m.Total()
	if total == 0 {
		return notEstimable(NotEstimableNoPairs, 0)
	}
	correct := 0
	for index := range m.Counts {
		correct += m.Counts[index][index]
	}
	return estimated(float64(correct)/float64(total), total)
}

// ExpectedAgreement is the chance agreement implied by the marginals.
func (m ConfusionMatrix) ExpectedAgreement() Estimate {
	total := m.Total()
	if total == 0 {
		return notEstimable(NotEstimableNoPairs, 0)
	}
	expected := 0.0
	for _, label := range AllLabels() {
		expected += (float64(m.HumanCount(label)) / float64(total)) * (float64(m.PredictionCount(label)) / float64(total))
	}
	return estimated(expected, total)
}

// Kappa is Cohen's kappa: (observed - expected) / (1 - expected).
//
// A constant prediction does not by itself make the denominator zero, so a
// constant scorer yields a real kappa (0 against balanced labels) rather than
// not_estimable. Only a degenerate denominator is not_estimable.
func (m ConfusionMatrix) Kappa() Estimate {
	observed := m.Agreement()
	if observed.Status == EstimateNotEstimable {
		return observed
	}
	expected := m.ExpectedAgreement()
	if expected.Status == EstimateNotEstimable {
		return expected
	}
	denominator := 1 - *expected.Value
	if denominator <= 0 {
		return notEstimable(NotEstimableZeroExpectedAgreement, observed.N)
	}
	return estimated((*observed.Value-*expected.Value)/denominator, observed.N)
}

// Recall is the per-class recall (sensitivity) for one human label class.
func (m ConfusionMatrix) Recall(label Label) Estimate {
	row := labelIndex(label)
	if row < 0 {
		return notEstimable(NotEstimableEmptyClassDenominator, 0)
	}
	denominator := m.HumanCount(label)
	if denominator == 0 {
		return notEstimable(NotEstimableEmptyClassDenominator, 0)
	}
	return estimated(float64(m.Counts[row][row])/float64(denominator), denominator)
}

// Wilson computes the Wilson score interval for a binomial proportion. It is
// used for agreement and recall on pre-registered independent representatives,
// where the normal approximation misbehaves at small n or extreme proportions.
func Wilson(successes, n int, level float64) (Interval, error) {
	if n <= 0 {
		return Interval{}, Reject(ReasonInvalidMaterial, "wilson interval requires a positive denominator")
	}
	if successes < 0 || successes > n {
		return Interval{}, Reject(ReasonInvalidMaterial, "wilson successes must be within [0,n]")
	}
	z := normalQuantile(1 - (1-level)/2)
	proportion := float64(successes) / float64(n)
	zSquared := z * z
	denominator := 1 + zSquared/float64(n)
	center := (proportion + zSquared/(2*float64(n))) / denominator
	half := (z / denominator) * math.Sqrt(proportion*(1-proportion)/float64(n)+zSquared/(4*float64(n)*float64(n)))
	low, high := center-half, center+half
	if low < 0 {
		low = 0
	}
	if high > 1 {
		high = 1
	}
	return Interval{Low: low, High: high, Level: level}, nil
}

// AgreementInterval is the Wilson interval for observed agreement, counted on
// the same pairs the agreement point estimate used.
func (m ConfusionMatrix) AgreementInterval(level float64) (Interval, error) {
	total := m.Total()
	if total == 0 {
		return Interval{}, Reject(ReasonInvalidMaterial, "agreement interval requires paired predictions")
	}
	correct := 0
	for index := range m.Counts {
		correct += m.Counts[index][index]
	}
	return Wilson(correct, total, level)
}

// GroupPairs is the set of paired predictions that share one independent
// material group. Resampling happens at this level so same-source variants
// cannot masquerade as independent trials and narrow the interval.
type GroupPairs struct {
	GroupID string      `json:"group_id"`
	Pairs   []LabelPair `json:"pairs"`
}

// BootstrapRule is the pre-registered resampling policy.
type BootstrapRule struct {
	Resamples int     `json:"resamples"`
	Seed      int64   `json:"seed"`
	Level     float64 `json:"level"`
	// MaxInvalidFraction bounds how many resamples may fail to produce a defined
	// kappa before the interval is refused. Degenerate resamples are never
	// silently dropped to manufacture a green result.
	MaxInvalidFraction float64 `json:"max_invalid_fraction"`
}

// BootstrapResult reports the resampling outcome, including the counts that
// make it reproducible and the handling of degenerate draws.
type BootstrapResult struct {
	Interval  *Interval          `json:"interval"`
	Status    EstimateStatus     `json:"status"`
	Reason    NotEstimableReason `json:"reason,omitempty"`
	Resamples int                `json:"resamples"`
	Seed      int64              `json:"seed"`
	Level     float64            `json:"level"`
	Effective int                `json:"effective"`
	Invalid   int                `json:"invalid"`
}

// GroupBootstrapKappa resamples independent material groups with replacement and
// returns the percentile interval of Cohen's kappa.
//
// Determinism: the source is seeded only with rule.Seed, so the same pairs, seed
// and resample count always produce the same interval.
func GroupBootstrapKappa(groups []GroupPairs, rule BootstrapRule) BootstrapResult {
	result := BootstrapResult{
		Resamples: rule.Resamples, Seed: rule.Seed, Level: rule.Level,
		Status: EstimateNotEstimable, Reason: NotEstimableNoValidBootstrapResample,
	}
	if rule.Resamples <= 0 || len(groups) == 0 {
		return result
	}
	random := rand.New(rand.NewSource(rule.Seed))
	values := make([]float64, 0, rule.Resamples)
	invalid := 0
	for draw := 0; draw < rule.Resamples; draw++ {
		matrix := NewConfusionMatrix()
		for pick := 0; pick < len(groups); pick++ {
			group := groups[random.Intn(len(groups))]
			for _, pair := range group.Pairs {
				if err := matrix.Add(pair.Human, pair.Judge); err != nil {
					invalid++
					break
				}
			}
		}
		kappa := matrix.Kappa()
		if kappa.Status != EstimateEstimated {
			invalid++
			continue
		}
		values = append(values, *kappa.Value)
	}
	result.Effective = len(values)
	result.Invalid = invalid
	if len(values) == 0 {
		return result
	}
	if fraction := float64(invalid) / float64(rule.Resamples); fraction > rule.MaxInvalidFraction {
		// Too many degenerate draws: the interval is refused instead of being
		// computed from the surviving draws only.
		return result
	}
	sort.Float64s(values)
	level := rule.Level
	if level <= 0 || level >= 1 {
		level = DefaultIntervalLevel
	}
	low := percentile(values, (1-level)/2)
	high := percentile(values, 1-(1-level)/2)
	result.Interval = &Interval{Low: low, High: high, Level: level}
	result.Status = EstimateEstimated
	result.Reason = ""
	return result
}

// percentile interpolates the requested quantile of an ascending sample.
func percentile(sorted []float64, quantile float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	if quantile <= 0 {
		return sorted[0]
	}
	if quantile >= 1 {
		return sorted[len(sorted)-1]
	}
	position := quantile * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	weight := position - float64(lower)
	return sorted[lower] + (sorted[upper]-sorted[lower])*weight
}

// normalQuantile is the inverse standard normal CDF (Acklam's rational
// approximation, |error| < 1.15e-9). It keeps the interval formula free of a
// third-party statistics dependency.
func normalQuantile(probability float64) float64 {
	if probability <= 0 {
		return math.Inf(-1)
	}
	if probability >= 1 {
		return math.Inf(1)
	}
	const (
		a1   = -3.969683028665376e+01
		a2   = 2.209460984245205e+02
		a3   = -2.759285104469687e+02
		a4   = 1.383577518672690e+02
		a5   = -3.066479806614716e+01
		a6   = 2.506628277459239e+00
		b1   = -5.447609879822406e+01
		b2   = 1.615858368580409e+02
		b3   = -1.556989798598866e+02
		b4   = 6.680131188771972e+01
		b5   = -1.328068155288572e+01
		c1   = -7.784894002430293e-03
		c2   = -3.223964580411365e-01
		c3   = -2.400758277161838e+00
		c4   = -2.549732539343734e+00
		c5   = 4.374664141464968e+00
		c6   = 2.938163982698783e+00
		d1   = 7.784695709041462e-03
		d2   = 3.224671290700398e-01
		d3   = 2.445134137142996e+00
		d4   = 3.754408661907416e+00
		low  = 0.02425
		high = 1 - low
	)
	switch {
	case probability < low:
		q := math.Sqrt(-2 * math.Log(probability))
		return (((((c1*q+c2)*q+c3)*q+c4)*q+c5)*q + c6) /
			((((d1*q+d2)*q+d3)*q+d4)*q + 1)
	case probability <= high:
		q := probability - 0.5
		r := q * q
		return (((((a1*r+a2)*r+a3)*r+a4)*r+a5)*r + a6) * q /
			(((((b1*r+b2)*r+b3)*r+b4)*r+b5)*r + 1)
	default:
		q := math.Sqrt(-2 * math.Log(1-probability))
		return -(((((c1*q+c2)*q+c3)*q+c4)*q+c5)*q + c6) /
			((((d1*q+d2)*q+d3)*q+d4)*q + 1)
	}
}

// PairMetrics is the per-split statistic block.
type PairMetrics struct {
	Pairs               int                `json:"pairs"`
	Groups              int                `json:"groups"`
	Matrix              ConfusionMatrix    `json:"matrix"`
	Agreement           Estimate           `json:"agreement"`
	AgreementCI         *Interval          `json:"agreement_wilson"`
	ExpectedAgreement   Estimate           `json:"expected_agreement"`
	Kappa               Estimate           `json:"kappa"`
	KappaCI             BootstrapResult    `json:"kappa_bootstrap"`
	Recall              map[Label]Estimate `json:"recall"`
	HumanCounts         map[Label]int      `json:"human_counts"`
	PredictionCounts    map[Label]int      `json:"prediction_counts"`
	DistinctHumanLabels int                `json:"distinct_human_labels"`
	DistinctJudgeLabels int                `json:"distinct_judge_labels"`
}

// ComputePairMetrics builds the full statistic block for one set of pairs.
func ComputePairMetrics(groups []GroupPairs, rule BootstrapRule) (PairMetrics, error) {
	matrix := NewConfusionMatrix()
	pairs := 0
	activeGroups := 0
	predicted := map[Label]bool{}
	human := map[Label]bool{}
	for _, group := range groups {
		if len(group.Pairs) == 0 {
			continue
		}
		activeGroups++
		for _, pair := range group.Pairs {
			if err := matrix.Add(pair.Human, pair.Judge); err != nil {
				return PairMetrics{}, err
			}
			pairs++
			predicted[pair.Judge] = true
			human[pair.Human] = true
		}
	}
	metrics := PairMetrics{
		Pairs: pairs, Groups: activeGroups, Matrix: matrix,
		Agreement: matrix.Agreement(), ExpectedAgreement: matrix.ExpectedAgreement(),
		Kappa: matrix.Kappa(), Recall: map[Label]Estimate{},
		HumanCounts: map[Label]int{}, PredictionCounts: map[Label]int{},
		DistinctHumanLabels: len(human), DistinctJudgeLabels: len(predicted),
	}
	if interval, err := matrix.AgreementInterval(rule.Level); err == nil {
		metrics.AgreementCI = &interval
	}
	for _, label := range AllLabels() {
		metrics.Recall[label] = matrix.Recall(label)
		metrics.HumanCounts[label] = matrix.HumanCount(label)
		metrics.PredictionCounts[label] = matrix.PredictionCount(label)
	}
	metrics.KappaCI = GroupBootstrapKappa(groups, rule)
	return metrics, nil
}

// IsDegenerate reports the two shapes that must never be accepted as usable: a
// single human class, or a constant prediction.
func (m PairMetrics) IsDegenerate() (string, bool) {
	if m.Pairs == 0 {
		return "no_pairs", true
	}
	if m.DistinctHumanLabels < 2 {
		return "single_class_human_labels", true
	}
	if m.DistinctJudgeLabels < 2 {
		return "constant_prediction", true
	}
	return "", false
}
