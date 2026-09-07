You are working on Honryu (Go backend + React/TS web).

REPO: /home/coder/personal/honryu
BRANCH: feat/phase29-thresholds (already checked out, off develop @ dce92fa)

Goal: Phase 29 — thresholds/criteria verdict-at-a-glance (k6-style).

CONTEXT (verified recon):
- internal/domain/loadprofile/loadprofile.go: Profile.Criteria []string (json "criteria,omitempty") — Taurus pass/fail expressions like "failures>10%", "p95>500ms".
- internal/app/executionapp/service.go: StoreConfig persists criteria (line ~224); CriteriaFor(executionID) reads them (line ~238, used by GetExecutionConfig which wraps into Wrapper{Content: Profile{...}}).
- internal/domain/report/criteria.go: Report.EvaluateCriteria(criteria []string) []FailedCriterion — PURE, tested. FailedCriterion{Criterion string, Unparsed bool}.
- internal/adapters/httpapi/report_handlers.go: runReport (line 54) currently returns h.deps.Reports.GetReport(...) verbatim.
- internal/adapters/httpapi/router.go: Deps has Executions *executionapp.Service (line 39). Route GET /api/runs/{run_id}/report at line 218.
- web/src/api/reports.ts: Report interface (line 40) — add criteria_evaluation field.
- web/src/components/ExecutionConfigCard.tsx: exists (phase 27), GET unwraps {multi-test}, PUTs BARE profile. ConfigTest + ExecutionConfig interfaces in web/src/api/executionsConfig.ts.
- web/src/pages/Reports.tsx: ReportDetail with Tabs (phase 28), Overview tab panel at ~line 887.
- Criterion grammar (subset): ^\s*([a-zA-Z0-9_-]+)\s*(>=|<=|==|>|<|=)\s*([0-9]+(\.[0-9]+)?)\s*(%|ms|s)?\s*$ — subjects "failures"/"fail" (% threshold required) and "pNN" (ms default).

TASKS (one commit each, gates green between):

T1 — backend: criteria evaluation on the run report
- internal/adapters/httpapi/report_handlers.go: wrap runReport's response.
  After GetReport succeeds and authorizeReport passes, fetch criteria:
    crits, err := h.deps.Executions.CriteriaFor(r.Context(), rep.ExecutionID)
  On err → proceed with nil criteria (evaluation is additive, never blocks).
  Response shape: keep every existing field verbatim, ADD:
    "criteria": <[]string as configured, nil-safe>,
    "failing_criteria": <[]{criterion, unparsed} — EvaluateCriteria result, nil-safe>
  Use a local response struct embedding/wrapping the domain Report with the
  two added json fields (marshal order irrelevant). Do NOT touch domain types.
- Handler test in internal/adapters/httpapi/ (follow existing report_handlers_test.go patterns): configured criteria + a report whose p95 trips → failing_criteria names it; no criteria → both keys nil/absent; CriteriaFor error → 200 still.
- Run: go test ./internal/... — all green.

T2 — web: Criteria editor in ExecutionConfigCard
- web/src/api/executionsConfig.ts: ExecutionConfig gains criteria?: string[].
- ExecutionConfigCard.tsx: below the tests table, a "Pass/fail criteria" section:
  list of criteria rows (text + remove ✕ button), add-input + "Add" button,
  grammar hint text "e.g. failures>10%, p95>500ms". Draft state joins the
  card's existing dirty/save flow (PUT includes criteria: [] when all removed
  — send empty array, not undefined, so removals persist).
  data-testid: criteria-row-{i}, criteria-remove-{i}, criteria-input,
  criteria-add.
- Test: ExecutionConfigCard.test.tsx — add + save round-trip includes
  criteria in PUT body; remove sends criteria: [].

T3 — web: Thresholds card in Run Detail Overview tab
- web/src/api/reports.ts Report: criteria?: string[] | null; failing_criteria?:
  {criterion: string, unparsed: boolean}[] | null. Normalize nulls to [].
- Reports.tsx Overview tab (first panel): new Card "Thresholds" ABOVE Load:
  - if criteria null/empty → "No criteria configured." + hint to add in
    the execution's Configuration card.
  - else rows: ✅ pass (configured but NOT in failing), ❌ fail (failing,
    unparsed=false), ❓ unparsed (unparsed=true) with the raw expression.
  data-testid: thresholds-card, threshold-row-{i}, threshold-pass-{i},
  threshold-fail-{i}, threshold-unparsed-{i}.
- Test in Reports.test.tsx: report with criteria ["failures>10%","p95>500ms"]
  and failing [{p95>500ms}] renders 1 pass + 1 fail.

GATES after each task:
  go test ./internal/... (T1)
  cd web && /home/coder/.bun/bin/bun run vitest run && /home/coder/.bun/bin/bun run tsc -b (T2, T3)

COMMIT STYLE:
  feat(httpapi): run report carries criteria evaluation
  feat(web): criteria editor in execution config
  feat(web): thresholds verdict card on run overview

No PR, no push — commits only. Verify vitest total grows and stays green.