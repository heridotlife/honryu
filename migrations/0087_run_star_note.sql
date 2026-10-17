-- 0087_run_star_note: run starring and free-text run notes (phase 107).
--
-- Evidence annotation, k6-parity: a starred run is a RunCompare baseline
-- candidate (the picker sorts starred first, a lone star preselects), and a
-- note says why a run matters long after the engines are gone. Both are
-- post-hoc annotations on the stored report -- SaveReport always writes the
-- defaults (unstarred, no note) and the first-write-wins rule is untouched;
-- PUT /api/runs/{run_id}/star and /note are the only writers. Existing rows
-- read unstarred with no note: starred defaults 0 and note stays NULL, which
-- the read path maps to the empty string (the correlation-id convention).
ALTER TABLE execution_report
    ADD COLUMN starred TINYINT(1) NOT NULL DEFAULT 0 AFTER soak_trend,
    ADD COLUMN note TEXT NULL AFTER starred;
