// Package report turns the measurements a run produced into what is said about
// it afterwards.
//
// A report outlives the engines, the metrics backend's retention, and the
// campaign it belonged to, because it is the evidence a readiness judgement was
// made on. Percentiles come from the merged buckets of every pod, since a
// sharded run's latency cannot be recovered from the pods' own percentiles.
//
// Pure domain: no I/O.
package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/heridotlife/honryu/internal/domain/metrics"
	"github.com/heridotlife/honryu/internal/domain/taurus"
)

// TotalLabel is the label bzt uses for its own aggregate across all requests.
// It is excluded from a report: counting it beside the requests it summarises
// would double every total.
const TotalLabel = "__total__"

// Validation errors. Callers compare with errors.Is.
var (
	ErrExecutionRequired = errors.New("report: a valid execution id is required")
	ErrRunRequired       = errors.New("report: a valid run id is required")
	ErrOutcomeUnknown    = errors.New("report: unknown outcome")
)

// Percentiles maps a percentile to the response time at it.
//
// Like a Histogram it needs explicit marshalling: Go cannot marshal a
// float-keyed map, and JSON object keys must be strings. A report crosses both
// the API and the database, so without this it could be built and never served.
type Percentiles map[float64]float64

// MarshalJSON writes percentiles keyed by their number ("95": 0.2).
func (p Percentiles) MarshalJSON() ([]byte, error) {
	out := make(map[string]float64, len(p))
	for pct, v := range p {
		out[strconv.FormatFloat(pct, 'g', -1, 64)] = v
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads percentiles keyed by their number.
func (p *Percentiles) UnmarshalJSON(data []byte) error {
	var raw map[string]float64
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("report: percentiles: %w", err)
	}
	out := make(Percentiles, len(raw))
	for key, v := range raw {
		pct, err := strconv.ParseFloat(key, 64)
		if err != nil {
			return fmt.Errorf("report: percentile key %q is not a number: %w", key, err)
		}
		out[pct] = v
	}
	*p = out
	return nil
}

// reportedPercentiles are the percentiles every report carries.
var reportedPercentiles = []float64{50, 90, 95, 99}

// Load is a rate of work: what was asked for, or what was produced.
type Load struct {
	// Concurrency is virtual users.
	Concurrency int `json:"concurrency"`
	// Throughput is requests per second. Zero means unlimited when requested,
	// and nothing measured when achieved.
	Throughput float64 `json:"throughput"`
	// DurationSeconds is how long the load held: as long as it was meant to when
	// requested, as long as it actually did when achieved. The two differ for a
	// run that was aborted or died early, and the achieved figure is what the
	// achieved rate is measured over.
	DurationSeconds int `json:"duration_seconds,omitempty"`
	// Samples and Failed count requests; only meaningful for achieved load.
	Samples int64 `json:"samples,omitempty"`
	Failed  int64 `json:"failed,omitempty"`
}

// StatusLabel is one HTTP status a label's requests returned, and how often.
// Codes are consistent across engines where error messages are not, so they
// are what a report counts per request.
type StatusLabel struct {
	Code  string `json:"code"`
	Count int64  `json:"count"`
}

// LabelSummary is one request's share of a run. A service owner needs to know
// which request degraded, not only that something did.
type LabelSummary struct {
	Label     string      `json:"label"`
	Samples   int64       `json:"samples"`
	Failed    int64       `json:"failed"`
	ErrorRate float64     `json:"error_rate"`
	Latency   Percentiles `json:"latency"`
	// Statuses counts each HTTP status the label's requests returned, dominant
	// first. Nil when none were counted -- a run whose engine reports no
	// response codes, or a report from before they were accumulated.
	Statuses []StatusLabel `json:"statuses,omitempty"`
}

// Meta is everything about a run that its measurements do not carry: which run
// it was, what it was asked to do, and how it ended.
type Meta struct {
	ExecutionID int64
	ScenarioID  int64
	RunID       int64
	Engine      taurus.Executor
	// Cluster is the load origin: the registered cluster this run generated
	// load from (empty = the deployment default), sourced from the execution.
	Cluster string
	// CorrelationID is the trace id the run's load carried in its
	// traceparent/baggage headers -- the id to deep-link into a customer's own
	// APM with. Empty for runs that predate it.
	CorrelationID string
	StartedAt     time.Time
	EndedAt       time.Time
	// Requested is the load the execution asked for.
	Requested Load
	// Outcome is how the run ended.
	Outcome taurus.Outcome
}

// achievedSeconds is how long the run actually produced load.
//
// The requested duration is what was asked for, not what happened. A run aborted
// after ten seconds of a requested minute did not hold load for a minute, and
// dividing its samples by sixty understates the rate it really reached -- then
// ShortOfRequest would report a shortfall for a run that was meeting its target
// when it stopped, which is the opposite of what a reader needs to know.
//
// The wall clock is used where the caller supplied it, and the requested
// duration stands in where it did not.
func (m Meta) achievedSeconds() int {
	if !m.StartedAt.IsZero() && m.EndedAt.After(m.StartedAt) {
		if secs := int(m.EndedAt.Sub(m.StartedAt).Round(time.Second).Seconds()); secs > 0 {
			return secs
		}
	}
	return m.Requested.DurationSeconds
}

// Input is everything a report is built from in one pass.
type Input struct {
	ExecutionID int64
	ScenarioID  int64
	RunID       int64
	Engine      taurus.Executor
	// Cluster is the load origin, sourced from the execution (empty = default).
	Cluster string
	// CorrelationID is the trace id the run's load carried, sourced from the
	// run's own history (empty = the run predated it).
	CorrelationID string
	StartedAt     time.Time
	EndedAt       time.Time
	// Requested is the load the execution asked for.
	Requested Load
	// Outcome is how the run ended.
	Outcome taurus.Outcome
	// Intervals are every second every pod reported.
	Intervals []metrics.Interval
}

// Report is what a run is judged on.
type Report struct {
	ExecutionID int64           `json:"execution_id"`
	ScenarioID  int64           `json:"scenario_id"`
	RunID       int64           `json:"run_id"`
	Engine      taurus.Executor `json:"engine,omitempty"`
	// Cluster is the load origin surfaced to a reader (empty = default).
	Cluster string `json:"cluster,omitempty"`
	// CorrelationID is the trace id the run's load carried in its
	// traceparent/baggage headers: the deep-link id into a customer's own
	// APM for exactly this run (empty = the run predated it).
	CorrelationID string         `json:"correlation_id,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
	EndedAt       time.Time      `json:"ended_at"`
	Outcome       taurus.Outcome `json:"outcome"`

	// Requested and Achieved sit side by side deliberately: latency means little
	// without knowing whether the load that produced it was the load intended.
	Requested Load `json:"requested"`
	Achieved  Load `json:"achieved"`

	// ErrorRate counts every failure, whoever caused it. It is not a verdict
	// input on its own -- see TargetErrorRate, which counts only the target's.
	ErrorRate float64     `json:"error_rate"`
	Latency   Percentiles `json:"latency"`

	// Attribution splits the failures by who caused them, and Errors states the
	// side of each one, so no count of failures is ever reported without saying
	// where it came from.
	Attribution Attribution      `json:"attribution"`
	Errors      []ErrorSignature `json:"errors,omitempty"`

	Labels []LabelSummary `json:"labels,omitempty"`
}

// Meta is the run's identity and intent, without its measurements.
func (in Input) Meta() Meta {
	return Meta{
		ExecutionID:   in.ExecutionID,
		ScenarioID:    in.ScenarioID,
		RunID:         in.RunID,
		Engine:        in.Engine,
		Cluster:       in.Cluster,
		CorrelationID: in.CorrelationID,
		StartedAt:     in.StartedAt,
		EndedAt:       in.EndedAt,
		Requested:     in.Requested,
		Outcome:       in.Outcome,
	}
}

// Build summarises a run in one pass.
//
// It is the same computation an Accumulator performs incrementally, expressed
// for callers that hold every interval already -- tests, and anything rebuilding
// a report from measurements it has kept.
func Build(in Input) Report {
	acc := NewAccumulator()
	for _, iv := range in.Intervals {
		acc.Add(iv)
	}
	return acc.Report(in.Meta())
}

// ShortOfRequest reports whether the run produced materially less load than it
// was asked for.
//
// It matters because latency measured under a fraction of the intended load says
// little about how the target behaves under the real thing -- a reader who
// misses that would draw a confident conclusion from a run that never happened
// as designed.
func (r Report) ShortOfRequest() bool {
	if r.Requested.Throughput <= 0 {
		return false // unlimited: there is no target rate to fall short of
	}
	const tolerance = 0.95
	return r.Achieved.Throughput < r.Requested.Throughput*tolerance
}

// Validate checks a report can be stored and read back meaningfully.
func (r Report) Validate() error {
	switch {
	case r.ExecutionID <= 0:
		return ErrExecutionRequired
	case r.RunID <= 0:
		return ErrRunRequired
	}
	switch r.Outcome {
	case taurus.OutcomePassed, taurus.OutcomeFailed, taurus.OutcomeAborted, taurus.OutcomeError:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrOutcomeUnknown, r.Outcome)
	}
}

func rate(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

func perSecond(samples int64, seconds int) float64 {
	if seconds <= 0 {
		return 0
	}
	return float64(samples) / float64(seconds)
}
