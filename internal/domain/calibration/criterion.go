package calibration

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrCriterionInvalid rejects a target-health criterion whose expressions
// fall outside the Taurus grammar below. Callers compare with errors.Is.
var ErrCriterionInvalid = errors.New("calibration: criterion must be Taurus expressions like \"failures>10%\", \"p95>500ms\" (comma or newline separated)")

// criterionExpression is the calibration criterion's contract (phase 42's
// hotfix): one Taurus pass/fail expression, in exactly the subject set
// Taurus engines themselves support. Found live in phase 39's modal: prose
// like "error_rate < 0.01 AND p95 < 500ms" sailed through the handler,
// landed in the compiled config, and was rejected by bzt at run time
// ("Config Error: Unsupported fail criteria subject: error_rate") -- the
// cheapest place to say no is the one before anything is stored. No parser
// existed to reuse: report.EvaluateCriteria's pattern is an evaluator
// grammar (any word subject, evaluated or marked unparsed), not a
// validator -- it accepts exactly the prose this exists to reject.
var criterionExpression = regexp.MustCompile(`^(failures|p95|p50|avg-rt|concurrency)\s*(>|<|>=|<=)\s*[0-9.]+(ms|s|%|)?$`)

// SplitCriterion splits a criterion field into its individual expressions:
// comma or newline separated, surrounding whitespace trimmed, empties
// dropped. The storage and evaluation unit is ONE expression per entry
// (compile turns each stored criterion into one passfail entry), while the
// wire and the UI carry the list as one field.
func SplitCriterion(raw string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' }) {
		if expr := strings.TrimSpace(part); expr != "" {
			out = append(out, expr)
		}
	}
	return out
}

// ValidateCriterion rejects raw unless it holds at least one expression and
// every expression matches the grammar above. The reject is at creation,
// where fixing the input costs one field, not a run.
func ValidateCriterion(raw string) error {
	exprs := SplitCriterion(raw)
	if len(exprs) == 0 {
		return ErrCriterionInvalid
	}
	for _, expr := range exprs {
		if !criterionExpression.MatchString(expr) {
			return fmt.Errorf("%w: unsupported expression %q", ErrCriterionInvalid, expr)
		}
	}
	return nil
}
