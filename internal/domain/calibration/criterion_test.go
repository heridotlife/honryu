package calibration

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestValidateCriterion(t *testing.T) {
	t.Parallel()
	valid := []string{
		"failures>10%",
		"failures>5%",
		"p95>500ms",
		"p50<200ms",
		"avg-rt>1s",
		"concurrency<100",
		"failures>=0.5%",
		"p95 <= 300ms", // tolerant spacing
		// The hotfix's own two-expression default: the list grammar.
		"failures>10%, p95>500ms",
		"failures>10%\np95>500ms", // newline separator too
	}
	for _, raw := range valid {
		if err := ValidateCriterion(raw); err != nil {
			t.Errorf("ValidateCriterion(%q) = %v, want accepted", raw, err)
		}
	}

	invalid := map[string]string{
		// The prose that started this: Taurus rejects it at run time with
		// "Unsupported fail criteria subject: error_rate".
		"error_rate < 0.01 AND p95 < 500ms": "unsupported subject",
		"error_rate < 0.01":                 "unsupported subject",
		// Right subject, wrong shape.
		"failures>10% AND p95>500ms": "unsupported shape",
		"p95":                        "no operator",
		">500ms":                     "no subject",
		"p95>":                       "no threshold",
		// Unknown subjects.
		"latency>500ms": "unsupported subject",
		"p99>500ms":     "not in the contract's subject set",
		// Empty and separator-only input.
		"":     "empty",
		" , ":  "separators only",
		"\n\n": "separators only",
	}
	for raw := range invalid {
		if err := ValidateCriterion(raw); !errors.Is(err, ErrCriterionInvalid) {
			t.Errorf("ValidateCriterion(%q) = %v, want ErrCriterionInvalid", raw, err)
		}
	}

	// The error names the offending expression, so the operator can see
	// which of several needs fixing.
	err := ValidateCriterion("failures>10%, error_rate < 0.01")
	if err == nil || !errors.Is(err, ErrCriterionInvalid) {
		t.Fatalf("mixed list = %v, want ErrCriterionInvalid", err)
	}
	if got := err.Error(); !strings.Contains(got, "error_rate < 0.01") {
		t.Errorf("error %q does not name the offending expression", got)
	}
}

func TestSplitCriterion(t *testing.T) {
	t.Parallel()
	cases := []struct {
		raw  string
		want []string
	}{
		{"failures>10%", []string{"failures>10%"}},
		{"failures>10%, p95>500ms", []string{"failures>10%", "p95>500ms"}},
		{"failures>10%,p95>500ms", []string{"failures>10%", "p95>500ms"}},
		{"failures>10%\np95>500ms", []string{"failures>10%", "p95>500ms"}},
		{"  failures>10% ,  p95>500ms  ", []string{"failures>10%", "p95>500ms"}},
		{" , , ", nil},
		{"", nil},
	}
	for _, tc := range cases {
		if got := SplitCriterion(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SplitCriterion(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
