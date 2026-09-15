package slo

import (
	"errors"
	"testing"
	"time"
)

func f(v float64) *float64 { return &v }

func TestValidate(t *testing.T) {
	t.Parallel()
	valid := SLO{ProjectID: 7, Name: "checkout", TargetP95MS: f(250)}
	for _, tc := range []struct {
		name string
		slo  SLO
		want error
	}{
		{"ok p95 only", valid, nil},
		{"ok error rate only", SLO{ProjectID: 7, Name: "e", TargetErrorRate: f(0.01)}, nil},
		{"ok success ratio only", SLO{ProjectID: 7, Name: "s", TargetSuccessRatio: f(0.99)}, nil},
		{"ok all three", SLO{ProjectID: 7, Name: "a", TargetP95MS: f(1), TargetErrorRate: f(1), TargetSuccessRatio: f(0)}, nil},
		{"missing project", SLO{Name: "x", TargetP95MS: f(1)}, ErrProjectRequired},
		{"missing name", SLO{ProjectID: 7, TargetP95MS: f(1)}, ErrNameRequired},
		{"blank name", SLO{ProjectID: 7, Name: "   ", TargetP95MS: f(1)}, ErrNameRequired},
		{"name too long", SLO{ProjectID: 7, Name: string(make([]byte, MaxNameLength+1)), TargetP95MS: f(1)}, ErrNameTooLong},
		{"no targets", SLO{ProjectID: 7, Name: "x"}, ErrNoTargets},
		{"p95 zero", SLO{ProjectID: 7, Name: "x", TargetP95MS: f(0)}, ErrP95NotPositive},
		{"p95 negative", SLO{ProjectID: 7, Name: "x", TargetP95MS: f(-5)}, ErrP95NotPositive},
		{"p95 nan", SLO{ProjectID: 7, Name: "x", TargetP95MS: f(nan())}, ErrP95NotPositive},
		{"error rate over one", SLO{ProjectID: 7, Name: "x", TargetErrorRate: f(1.5)}, ErrErrorRateRange},
		{"error rate negative", SLO{ProjectID: 7, Name: "x", TargetErrorRate: f(-0.1)}, ErrErrorRateRange},
		{"success ratio over one", SLO{ProjectID: 7, Name: "x", TargetSuccessRatio: f(1.0001)}, ErrSuccessRatioRange},
		{"success ratio negative", SLO{ProjectID: 7, Name: "x", TargetSuccessRatio: f(-1)}, ErrSuccessRatioRange},
		{"success ratio boundary one", SLO{ProjectID: 7, Name: "x", TargetSuccessRatio: f(1)}, nil},
		{"error rate boundary one", SLO{ProjectID: 7, Name: "x", TargetErrorRate: f(1)}, nil},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.slo.Validate()
			if !errors.Is(err, tc.want) {
				t.Errorf("Validate() = %v, want %v", err, tc.want)
			}
		})
	}
}

func nan() float64 { n := 0.0; return n / n }

func TestParseWindow(t *testing.T) {
	t.Parallel()
	for raw, want := range map[string]time.Duration{
		Window1d:  24 * time.Hour,
		Window7d:  7 * 24 * time.Hour,
		Window30d: 30 * 24 * time.Hour,
	} {
		got, err := ParseWindow(raw)
		if err != nil {
			t.Errorf("ParseWindow(%q) = %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("ParseWindow(%q) = %v, want %v", raw, got, want)
		}
	}
	if _, err := ParseWindow("7"); !errors.Is(err, ErrWindowInvalid) {
		t.Errorf("ParseWindow(\"7\") = %v, want ErrWindowInvalid", err)
	}
	if _, err := ParseWindow(""); !errors.Is(err, ErrWindowInvalid) {
		t.Errorf("ParseWindow(\"\") = %v, want ErrWindowInvalid", err)
	}
	if _, err := ParseWindow("14d"); !errors.Is(err, ErrWindowInvalid) {
		t.Errorf("ParseWindow(\"14d\") = %v, want ErrWindowInvalid", err)
	}
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	t.Run("lowerIsBetterMetrics", func(t *testing.T) {
		t.Parallel()
		s := SLO{ProjectID: 7, Name: "x", TargetP95MS: f(200), TargetErrorRate: f(0.01)}
		got := s.Evaluate(Actual{P95MS: f(150), ErrorRate: f(0.008)})
		if len(got.Metrics) != 2 {
			t.Fatalf("metrics = %d lines, want 2 (only what the SLO targets)", len(got.Metrics))
		}
		p95 := got.Metrics[0]
		if p95.Metric != MetricP95MS || !p95.Compliant {
			t.Errorf("p95 line = %+v, want compliant p95_ms", p95)
		}
		// (200 - 150)/200 * 100 = 25
		if p95.BudgetRemainingPct == nil || *p95.BudgetRemainingPct != 25 {
			t.Errorf("p95 remaining = %v, want 25", p95.BudgetRemainingPct)
		}
		er := got.Metrics[1]
		if er.Metric != MetricErrorRate || !er.Compliant {
			t.Errorf("error-rate line = %+v, want compliant error_rate", er)
		}
		// (0.01 - 0.008)/0.01 * 100 = 20
		if er.BudgetRemainingPct == nil || *er.BudgetRemainingPct != 20 {
			t.Errorf("error-rate remaining = %v, want 20", er.BudgetRemainingPct)
		}
		if !got.Compliant {
			t.Error("budget = non-compliant, want compliant")
		}
	})

	t.Run("higherIsBetterMetric", func(t *testing.T) {
		t.Parallel()
		s := SLO{ProjectID: 7, Name: "x", TargetSuccessRatio: f(0.99)}
		got := s.Evaluate(Actual{SuccessRatio: f(0.995)})
		line := got.Metrics[0]
		// (0.995 - 0.99)/0.99 * 100 = 0.505050...
		if !line.Compliant {
			t.Errorf("line = %+v, want compliant", line)
		}
		if line.BudgetRemainingPct == nil || *line.BudgetRemainingPct <= 0.5 || *line.BudgetRemainingPct >= 0.51 {
			t.Errorf("remaining = %v, want ~0.505", line.BudgetRemainingPct)
		}
	})

	t.Run("violatedBudgetGoesNegative", func(t *testing.T) {
		t.Parallel()
		s := SLO{ProjectID: 7, Name: "x", TargetP95MS: f(200), TargetSuccessRatio: f(0.99)}
		got := s.Evaluate(Actual{P95MS: f(300), SuccessRatio: f(0.95)})
		if got.Compliant {
			t.Error("budget = compliant, want violated")
		}
		// (200 - 300)/200 * 100 = -50
		if p := got.Metrics[0].BudgetRemainingPct; p == nil || *p != -50 {
			t.Errorf("p95 remaining = %v, want -50", p)
		}
		if line := got.Metrics[1]; line.Compliant {
			t.Errorf("success-ratio line = %+v, want violated", line)
		}
		worst, ok := got.Worst()
		if !ok || worst.Metric != MetricP95MS {
			t.Errorf("worst = %+v ok=%v, want p95_ms (-50 < -4.04)", worst, ok)
		}
	})

	t.Run("boundaryExactlyAtTargetIsCompliant", func(t *testing.T) {
		t.Parallel()
		s := SLO{ProjectID: 7, Name: "x", TargetP95MS: f(200), TargetErrorRate: f(0.01), TargetSuccessRatio: f(0.99)}
		got := s.Evaluate(Actual{P95MS: f(200), ErrorRate: f(0.01), SuccessRatio: f(0.99)})
		if !got.Compliant {
			t.Error("exactly-at-target graded non-compliant; the boundary is inclusive")
		}
		for _, m := range got.Metrics {
			if m.BudgetRemainingPct == nil || *m.BudgetRemainingPct != 0 {
				t.Errorf("%s remaining = %v, want exactly 0", m.Metric, m.BudgetRemainingPct)
			}
		}
	})

	t.Run("noDataIsVacuouslyCompliantWithNoNumbers", func(t *testing.T) {
		t.Parallel()
		s := SLO{ProjectID: 7, Name: "x", TargetP95MS: f(200)}
		got := s.Evaluate(Actual{})
		if !got.Compliant {
			t.Error("no data graded non-compliant; absence of evidence is not a violation")
		}
		line := got.Metrics[0]
		if line.Actual != nil || line.BudgetRemainingPct != nil || !line.Compliant {
			t.Errorf("vacuous line = %+v, want compliant with nil actual/remaining", line)
		}
		if _, ok := got.Worst(); ok {
			t.Error("Worst found data in a no-data budget")
		}
	})
}

func TestWindowMean(t *testing.T) {
	t.Parallel()
	if got := WindowMean(nil); got != nil {
		t.Errorf("WindowMean(nil) = %v, want nil", got)
	}
	if got := WindowMean([]float64{}); got != nil {
		t.Errorf("WindowMean(empty) = %v, want nil", got)
	}
	got := WindowMean([]float64{100, 200, 300})
	if got == nil || *got != 200 {
		t.Errorf("WindowMean = %v, want 200", got)
	}
}
