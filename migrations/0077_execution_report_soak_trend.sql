-- 0077_execution_report_soak_trend: the soak leak finding on reports (phase 99).
--
-- soak_trend carries the latency trend a long steady window produced --
-- each half's mean response time, the per-second slope, and the suspected
-- leak verdict -- as JSON, mirroring the cluster_results additive-column
-- convention 0072 documents: NULL for every run whose window was too short
-- to trend (under two minutes of latency-carrying seconds, i.e. most runs),
-- present only where there is a finding. Phase 98's lesson is why this is
-- a tested column and not an assumed one: a report field the summary
-- projection forgets reads as "derived but silently dropped".
ALTER TABLE execution_report
    ADD COLUMN soak_trend JSON NULL AFTER cluster_results;
