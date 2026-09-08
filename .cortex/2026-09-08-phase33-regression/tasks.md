You are working on Honryu web (React/TS, bun, Tailwind).

REPO: /home/coder/personal/honryu
BRANCH: feat/phase33-regression-diff (checked out, off develop @ 5e232a5)

Goal: Phase 33 — regression surfacing + baseline run comparison. The backend
ALREADY computes per-run regression (GET /api/executions/{id}/trend returns
points with regressed, has_comparable_predecessor, hit_target_qps, p50/90/95/
99, error_rate, achieved_throughput). Nothing in the UI shows it. Pure
frontend phase — no Go changes.

VERIFIED RECON (live wire, exec 7):
- trend points: {run_id, outcome, achieved_throughput, requested_throughput,
  error_rate, p50, p90, p95, p99 (seconds), hit_target_qps,
  has_comparable_predecessor, regressed?} — newest first. regressed is
  omitempty (absent = false).
- web/src/api/trends.ts: TrendPoint/Trend types + getExecutionTrend(id, limit)
  already exist and are correct.
- web/src/pages/Reports.tsx: ReportDetail workspace (phase 28 tabs: Overview/
  Time series/Labels/Errors/Config/Objects), thresholds card (phase 29),
  labels table w/ status badges (phase 30).
- GET /api/runs/{id}/report → full Report incl labels []LabelSummary
  {label, samples, failed, error_rate, latency{50,90,95,99}, statuses?} —
  fetchable per-run for label-level diff.
- Nav DashboardLayout; project switcher localStorage key "honryu.project".

TASKS (one commit each, gates between):

T1 — Trend strip on Execution page: regression badges
- web/src/pages/Execution.tsx: below the execution header, add a compact
  "Recent runs" strip: last N (10) trend points from getExecutionTrend.
  Each run = small pill: run id + outcome color (passed=emerald, failed=rose,
  aborted=slate) + REGRESSED badge (rose, data-testid="trend-regressed-{id}")
  when regressed true; tooltip (title attr) "missed target QPS vs previous
  hit". data-testid="trend-strip", pills trend-pill-{id}. Newest first.
  Empty/error → hide strip.
- Test: pills render from mocked trend; regressed pill shows badge; absent
  regressed field → no badge.

T2 — Baseline comparison on Run Detail: overview card
- Reports.tsx ReportDetail: in Overview tab under Thresholds card, a
  "Compare" card (data-testid="compare-card"):
  - baseline picker: <select>-free — a small list popover (reuse popover
    pattern from ProjectSwitcher) of the execution's other runs (from
    listExecutionReports already fetched) labeled "#id · outcome · date".
    Default: previous passed run (the newest other run with outcome passed).
    data-testid="compare-baseline-{id}" options.
  - on pick: fetch both runs' reports (current already in memory), diff the
    headline metrics: samples, error_rate, p95, p99, rps (achieved_throughput).
    Render metric rows: name, current, baseline, delta with % — color rose if
    worse (p95/p99/error_rate up >10%, samples/rps down >10%), emerald if
    better, slate otherwise. data-testid="compare-metric-{name}".
  - "no comparable run" state → card hidden entirely.
- Test: mock two reports; assert deltas computed + colored; default baseline
  picks previous passed run.

T3 — Label-level diff (extend T2)
- Same Compare card: second table "Per-label p95 diff" — for labels present
  in BOTH runs: label, current p95 ms, baseline p95 ms, delta % (rose if
  >+10%, emerald <−10%, else slate). Labels only in current → "new" badge;
  only in baseline → "dropped" badge. data-testid="compare-label-{name}".
- Test: label present both → delta row; label only current → new badge.

GATES after each: cd web && /home/coder/.bun/bin/bun run vitest run &&
/home/coder/.bun/bin/bun run tsc -b

COMMIT STYLE:
  feat(web): trend strip with regression badges
  feat(web): baseline comparison card
  feat(web): label-level diff in comparison

No PR, no push. vitest total must grow from 389 and stay green. Latency
fields are SECONDS on the wire — render ms.