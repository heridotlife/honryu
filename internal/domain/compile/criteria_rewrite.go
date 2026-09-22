package compile

import "regexp"

// FloorCriteriaToViolation rewrites user-facing floor assertions into
// bzt's failure-condition grammar (phase 104).
//
// bzt's passfail criteria are failure conditions: "p95>500ms" fails the
// run when p95 exceeds 500ms. Users, and every criterion Suggested-
// Thresholds ever produced, write floors: "p95<800ms" -- pass when fast.
// Compiled literally, a floor reads as "fail when fast", so a perfectly
// healthy target trips it and the run's verdict is failed with zero
// errors (live run 1: 73850 samples, 0 failed, p95=2ms, outcome failed).
//
// The rewrite inverts exactly the floor comparisons a criterion may
// carry, leaving every other form byte-identical:
//
//	p95<800ms   -> p95>=800ms   (fails only when p95 reaches 800ms)
//	p95<=800ms  -> p95>800ms
//	failures<5% -> failures>=5%
//
// Both operators are inverted only when the comparison reads as a floor
// (a < or <= between subject and threshold). >, >=, ==, = are already
// failure conditions and pass through untouched, as does anything the
// pattern does not match -- the server-side evaluator
// (report.EvaluateCriteria) reports non-matching criteria as unparsed
// rather than guessing, and this rewrite keeps the same discipline:
// unknown shapes are returned unchanged.
//
// The result is what the compiled Taurus config carries and what the
// report layer evaluates, so the engine's exit code and the served
// verdict layer agree on one grammar. The original configured text is
// never mutated: callers keep storing what the user wrote; only the
// compiled form changes.
func FloorCriteriaToViolation(criteria []string) []string {
	if len(criteria) == 0 {
		return nil
	}
	out := make([]string, 0, len(criteria))
	for _, c := range criteria {
		out = append(out, floorToViolation(c))
	}
	return out
}

// floorCriterion matches the floor subset of the practical criteria
// grammar: subject, a strict-or-equal less-than, threshold, optional
// unit -- the same shape report.criterionPattern understands, so the
// two grammars cannot drift apart silently. Captures: 1 subject,
// 2 operator (< or <=), 3 threshold, 4 unit.
var floorCriterion = regexp.MustCompile(`^\s*([a-zA-Z0-9_-]+)\s*(<=|<)\s*([0-9]+(?:\.[0-9]+)?)\s*(%|ms|s)?\s*$`)

// floorToViolation rewrites one criterion, or returns it unchanged when
// it is not a floor form.
func floorToViolation(c string) string {
	m := floorCriterion.FindStringSubmatch(c)
	if m == nil {
		return c
	}
	subject, op, threshold, unit := m[1], m[2], m[3], m[4]
	var inverted string
	switch op {
	case "<":
		inverted = ">="
	case "<=":
		inverted = ">"
	default:
		// Unreachable: the pattern only matches < and <=.
		return c
	}
	return subject + inverted + threshold + unit
}
