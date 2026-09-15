// Package slo models a project's service-level objectives and the budget
// arithmetic that grades a window of run reports against them. Pure domain:
// no I/O, no storage -- the report fetching is sloapp's job, this package
// owns the shape of an SLO, the window grammar both sides of the wire share,
// and the one formula every budget number is derived from.
package slo

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Limits and target bounds, mirroring the 0066_project_slo columns so an SLO
// that validates can always be stored (and vice versa).
const (
	// MaxNameLength matches name VARCHAR(128).
	MaxNameLength = 128
	// Window1d/Window7d/Window30d are the query windows the budget endpoint
	// accepts, as the grammar words the API serves.
	Window1d  = "1d"
	Window7d  = "7d"
	Window30d = "30d"
	// DefaultWindow is what ?window= omitted buys: a week is long enough to
	// smooth one bad run and short enough to still be news.
	DefaultWindow = Window7d
)

// Validation errors. Callers compare with errors.Is.
var (
	ErrProjectRequired   = errors.New("slo: a valid project id is required")
	ErrNameRequired      = errors.New("slo: a name is required")
	ErrNameTooLong       = errors.New("slo: name must be at most 128 characters")
	ErrNoTargets         = errors.New("slo: at least one target is required")
	ErrP95NotPositive    = errors.New("slo: target_p95_ms must be a positive number of milliseconds")
	ErrErrorRateRange    = errors.New("slo: target_error_rate must be between 0 and 1")
	ErrSuccessRatioRange = errors.New("slo: target_success_ratio must be between 0 and 1")
	// ErrWindowInvalid rejects a window outside the supported grammar.
	ErrWindowInvalid = errors.New("slo: window must be one of 1d, 7d, 30d")
)

// SLO is one named objective: the targets an operator holds a project's
// service to. Any subset of the three targets may be set, but at least one
// must be -- an SLO with no targets grades nothing and could never be
// violated, which would make its badge a lie of absence.
type SLO struct {
	// ID is the storage-assigned row identity; zero before Create.
	ID int64
	// ProjectID is the project whose runs this objective grades.
	ProjectID int64
	// Name is the human-facing identity, unique within the project.
	Name string
	// TargetP95MS is the p95 response time ceiling in milliseconds (reports
	// keep seconds; this is the unit operators quote). Nil = not tracked.
	TargetP95MS *float64
	// TargetErrorRate is the mean per-run error-rate ceiling, 0..1. Nil =
	// not tracked.
	TargetErrorRate *float64
	// TargetSuccessRatio is the floor for passed / total non-aborted runs,
	// 0..1. Nil = not tracked.
	TargetSuccessRatio *float64
	// CreatedTime is when the objective was defined.
	CreatedTime time.Time
}

// Validate checks an SLO's own invariants, independent of persistence. The
// at-least-one-target rule is enforced here rather than in DDL so the rule
// and its error live in one place, and so a MySQL version that ignores CHECK
// constraints cannot silently accept an empty SLO.
func (s SLO) Validate() error {
	switch {
	case s.ProjectID <= 0:
		return ErrProjectRequired
	case strings.TrimSpace(s.Name) == "":
		return ErrNameRequired
	case len(s.Name) > MaxNameLength:
		return ErrNameTooLong
	}
	if s.TargetP95MS == nil && s.TargetErrorRate == nil && s.TargetSuccessRatio == nil {
		return ErrNoTargets
	}
	if s.TargetP95MS != nil && (*s.TargetP95MS <= 0 || math.IsNaN(*s.TargetP95MS) || math.IsInf(*s.TargetP95MS, 0)) {
		return ErrP95NotPositive
	}
	if s.TargetErrorRate != nil && bad01(s.TargetErrorRate) {
		return ErrErrorRateRange
	}
	if s.TargetSuccessRatio != nil && bad01(s.TargetSuccessRatio) {
		return ErrSuccessRatioRange
	}
	return nil
}

// bad01 reports whether v cannot be a ratio in [0, 1].
func bad01(v *float64) bool {
	return *v < 0 || *v > 1 || math.IsNaN(*v) || math.IsInf(*v, 0)
}

// ParseWindow validates raw as a supported budget window, returning its
// duration or ErrWindowInvalid. The boundary where operator input (the
// ?window= query value) becomes a span -- everything downstream may assume
// the word is one of the three.
func ParseWindow(raw string) (time.Duration, error) {
	switch raw {
	case Window1d:
		return 24 * time.Hour, nil
	case Window7d:
		return 7 * 24 * time.Hour, nil
	case Window30d:
		return 30 * 24 * time.Hour, nil
	default:
		return 0, ErrWindowInvalid
	}
}

// Metric names, as the budget's wire contract spells them. p95_ms names the
// unit deliberately: the target is stored in milliseconds while a report's
// latency map is seconds, and a reader of the JSON should never have to
// check which one a number is.
const (
	MetricP95MS        = "p95_ms"
	MetricErrorRate    = "error_rate"
	MetricSuccessRatio = "success_ratio"
)

// Actual is the window's measured side: what the reports say the service
// did. A field is nil when the metric was not measured -- either the SLO
// does not track it or the window held no eligible runs -- and a nil field
// never grades.
type Actual struct {
	// P95MS is the window's mean of per-run p95 latencies, converted from
	// reports' seconds to the SLO's milliseconds.
	P95MS *float64
	// ErrorRate is the window's mean of per-run error rates.
	ErrorRate *float64
	// SuccessRatio is passed runs over total non-aborted runs.
	SuccessRatio *float64
}

// MetricBudget is one target's grade: the target, what was measured, whether
// the measured value stayed within it, and how much of the budget the
// measured value leaves.
//
// budget_remaining_pct is THE formula, stated once so every reader shares it:
//
//	lower-is-better (p95_ms, error_rate): (target - actual)/target * 100
//	higher-is-better (success_ratio):     (actual - target)/target * 100
//
// i.e. (actual - target)/target * 100, sign-corrected so that positive
// always means "margin left", zero "sitting exactly on the target", and
// negative "burned past the target". Sign-corrected, not absolute-valued:
// an SLO at half its error budget and one at triple its target must not
// render as the same magnitude of trouble. Comparisons are plain float64 --
// the trend code's own convention (no epsilon; its tolerance appears as a
// scaled constant, not a fuzzy compare) -- and compliance is inclusive at
// the boundary: exactly-at-target is compliant with 0% remaining.
type MetricBudget struct {
	// Metric is one of the Metric* names above.
	Metric string
	// Target is the SLO's configured value (ms, or a 0..1 ratio).
	Target float64
	// Actual is the window's measured value in the target's own unit; nil
	// when the window held no eligible runs.
	Actual *float64
	// Compliant is whether actual stayed within target. Vacuously true
	// when the window held no eligible runs -- absence of evidence is not
	// a violation, the trend endpoint's own "no baseline is not a
	// regression" law.
	Compliant bool
	// BudgetRemainingPct is the formula above; nil when the window held no
	// eligible runs.
	BudgetRemainingPct *float64
}

// Budget is one SLO's grade over one window: the per-metric lines for every
// target the SLO configures, plus the overall verdict -- a window is
// compliant only when every tracked metric is.
type Budget struct {
	// Metrics carries one entry per configured target, in a stable order
	// (p95_ms, error_rate, success_ratio -- the columns' own order).
	Metrics []MetricBudget
	// Compliant is every metric's compliance conjoined. True when the SLO
	// has no data, as each vacuous line is true.
	Compliant bool
}

// Worst returns the metric carrying the least budget remaining -- the line a
// digest leads with. Metrics with no data sort last (a measured healthy
// metric beats an unmeasured one for "worst"); ties keep the stable order.
// Returns ok=false when no metric has data at all.
func (b Budget) Worst() (MetricBudget, bool) {
	worst := MetricBudget{}
	found := false
	for _, m := range b.Metrics {
		if m.Actual == nil || m.BudgetRemainingPct == nil {
			continue
		}
		if !found || *m.BudgetRemainingPct < *worst.BudgetRemainingPct {
			worst = m
			found = true
		}
	}
	return worst, found
}

// Evaluate grades actual against the SLO's configured targets. Metrics the
// SLO does not target produce no line; a window with no eligible runs (nil
// actual fields) produces vacuous lines: compliant, no numbers -- the honest
// reading of "nothing violated it, because nothing ran".
func (s SLO) Evaluate(actual Actual) Budget {
	b := Budget{Metrics: make([]MetricBudget, 0, 3), Compliant: true}
	line := func(metric string, target float64, got *float64, lowerIsBetter bool) {
		mb := MetricBudget{Metric: metric, Target: target, Compliant: true}
		if got != nil {
			mb.Actual = got
			var remaining float64
			if lowerIsBetter {
				remaining = (target - *got) / target * 100
				mb.Compliant = *got <= target
			} else {
				remaining = (*got - target) / target * 100
				mb.Compliant = *got >= target
			}
			mb.BudgetRemainingPct = &remaining
		}
		b.Metrics = append(b.Metrics, mb)
		b.Compliant = b.Compliant && mb.Compliant
	}
	if s.TargetP95MS != nil {
		line(MetricP95MS, *s.TargetP95MS, actual.P95MS, true)
	}
	if s.TargetErrorRate != nil {
		line(MetricErrorRate, *s.TargetErrorRate, actual.ErrorRate, true)
	}
	if s.TargetSuccessRatio != nil {
		line(MetricSuccessRatio, *s.TargetSuccessRatio, actual.SuccessRatio, false)
	}
	return b
}

// WindowMean returns the arithmetic mean of vals, or nil when vals is empty.
// The window's aggregate for a per-run metric: each run's report already
// merged its pods' buckets (a sharded p95 cannot be had from the shards'
// own percentiles), so the honest window reading is the run-count-weighted
// mean of those per-run figures -- not a p95 of p95s, which would treat the
// worst run's distribution as one sample among many.
func WindowMean(vals []float64) *float64 {
	if len(vals) == 0 {
		return nil
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	mean := sum / float64(len(vals))
	return &mean
}

// String renders an SLO for logs, name first -- the identity an operator
// knows it by.
func (s SLO) String() string {
	return fmt.Sprintf("%s (project %d)", s.Name, s.ProjectID)
}
