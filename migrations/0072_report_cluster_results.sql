-- 0072_report_cluster_results: the per-cluster breakdown of a fan-out run
-- (internal/domain/report.Report.ClusterResults, phase 88) -- one row per
-- target cluster that actually pushed measurements, with its share of the
-- run's samples and failures.
--
-- NULL on every single-cluster report (and on a fan-out report finalised
-- after a control-plane restart, whose in-memory tally is gone): the run's
-- own columns remain the run's whole truth; these rows add the origin split.
-- Additive and NULL-able, so every existing reader of execution_report --
-- the Reports API, trend queries, campaign verdicts -- is unaffected.
ALTER TABLE execution_report
    ADD COLUMN cluster_results JSON NULL AFTER labels;
