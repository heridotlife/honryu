package report_test

import (
	"regexp"
	"testing"

	"github.com/heridotlife/honryu/internal/domain/report"
)

// r's ErrorRate is 15% and its measured percentiles are p50=100ms,
// p95=600ms, p99=1.2s -- every test below computes its threshold relative
// to these fixed measurements. A criterion "triggers" (bzt's own
// passfail semantics: the run is judged to have failed) when its
// configured comparison holds against the measured value -- e.g.
// "failures>10%" triggers because 15% actually exceeds 10%.
func fixtureReport() report.Report {
	return report.Report{
		ErrorRate: 0.15,
		Latency:   report.Percentiles{50: 0.100, 95: 0.600, 99: 1.200},
	}
}

func TestReport_EvaluateCriteria(t *testing.T) {
	t.Parallel()
	r := fixtureReport()

	tests := []struct {
		name      string
		criterion string
		wantLen   int
		wantFail  bool // when wantLen == 1: a real trigger (true) or unparsed (false)?
	}{
		{"error-rate criterion that does not trigger (15% does not exceed 50%)", "failures>50%", 0, false},
		{"error-rate criterion that triggers, named exactly (15% exceeds 10%)", "failures>10%", 1, true},
		{"latency criterion in ms that does not trigger (600ms does not exceed 700ms)", "p95>700ms", 0, false},
		{"latency criterion in ms that triggers (600ms exceeds 500ms)", "p95>500ms", 1, true},
		{"latency criterion in s that triggers (1.2s exceeds 1s)", "p99>1s", 1, true},
		{"latency criterion in s that does not trigger (1.2s does not exceed 2s)", "p99>2s", 0, false},
		{"no unit suffix defaults to ms (600ms exceeds 500)", "p95>500", 1, true},
		{"unmeasured percentile is unparsed", "p90>500ms", 1, false},
		{"unsupported subject is unparsed", "avg-rt>500ms", 1, false},
		{"failures with no percent suffix is ambiguous, unparsed", "failures>10", 1, false},
		{"malformed syntax is unparsed", "failures greater than 10%", 1, false},
		{"bzt's fuller grammar (a 'for' window) is out of scope, unparsed", "failures>10% for 5s", 1, false},
		{"boundary: exactly-at-threshold with > does not trigger", "failures>15%", 0, false},
		{"boundary: exactly-at-threshold with >= triggers", "failures>=15%", 1, true},
		// Phase 104: less-than reads as a floor assertion (pass-when-under),
		// rewritten to its violation form before evaluation. A floor that
		// holds (15% < 50%) does not trigger; a violated floor (15% >= 10%)
		// triggers and is reported in its violation form.
		{"floor holds (15% is under 50%) does not trigger", "failures<50%", 0, false},
		{"floor violated (15% reaches 10%) triggers, reported as failures>=10%", "failures<10%", 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := r.EvaluateCriteria([]string{tt.criterion})
			if len(got) != tt.wantLen {
				t.Fatalf("EvaluateCriteria(%q) = %+v, want len %d", tt.criterion, got, tt.wantLen)
			}
			if tt.wantLen == 0 {
				return
			}
			// A floor criterion is judged (and reported) in its violation
			// form; everything else preserves its original text. The
			// grammar is compact (no spaces around the operator), matching
			// criterionPattern.
			want := tt.criterion
			if m := floorRe.FindStringSubmatch(tt.criterion); m != nil {
				inv := ">"
				if m[2] == "<" {
					inv = ">="
				}
				want = m[1] + inv + m[3] + m[4]
			}
			if got[0].Criterion != want {
				t.Errorf("Criterion = %q, want %q", got[0].Criterion, want)
			}
			if got[0].Unparsed == tt.wantFail {
				t.Errorf("Unparsed = %v, want %v", got[0].Unparsed, !tt.wantFail)
			}
		})
	}
}

// A criterion that did not trigger must not appear in the result at all --
// only what actually triggered (or couldn't be read) is ever named.
func TestReport_EvaluateCriteria_OmitsNonTriggeringCriteria(t *testing.T) {
	t.Parallel()
	r := report.Report{ErrorRate: 0.01, Latency: report.Percentiles{95: 0.100}}

	got := r.EvaluateCriteria([]string{"failures>10%", "p95>500ms", "failures>50%"})
	if len(got) != 0 {
		t.Fatalf("EvaluateCriteria = %+v, want none -- no criterion triggered", got)
	}
}

func TestReport_EvaluateCriteria_MultipleCriteriaEachEvaluatedIndependently(t *testing.T) {
	t.Parallel()
	r := report.Report{ErrorRate: 0.20, Latency: report.Percentiles{95: 1.000}} // 1000ms

	got := r.EvaluateCriteria([]string{"failures>10%", "p95<500ms", "failures>50%"})
	if len(got) != 2 {
		t.Fatalf("EvaluateCriteria = %+v, want exactly 2 (failures>10%% and the violated floor p95<500ms, failures>50%% does not)", got)
	}
	// The floor is reported in its violation form (p95>=500ms).
	criteria := map[string]bool{got[0].Criterion: true, got[1].Criterion: true}
	if !criteria["failures>10%"] || !criteria["p95>=500ms"] {
		t.Fatalf("EvaluateCriteria = %+v, want failures>10%% and p95>=500ms", got)
	}
}

func TestReport_EvaluateCriteria_EmptyInputReturnsEmpty(t *testing.T) {
	t.Parallel()
	r := report.Report{}
	if got := r.EvaluateCriteria(nil); len(got) != 0 {
		t.Fatalf("EvaluateCriteria(nil) = %+v, want none", got)
	}
}

// floorRe mirrors the domain's floor grammar (compact, no spaces) for the
// violation-form expectation above.
var floorRe = regexp.MustCompile(`^\s*([a-zA-Z0-9_-]+)\s*(<=|<)\s*([0-9]+(?:\.[0-9]+)?)\s*(%|ms|s)?\s*$`)
