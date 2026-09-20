-- 0076_execution_report_steps: staircase step tables on reports (phase 98).
--
-- requested_steps carries the staircase's plateau table (index, rate,
-- users, hold) as JSON, mirroring the cluster_results additive-column
-- convention 0072 documents: NULL for every flat request, present only
-- for stepped ones. Without it the summary projection round-trips the
-- ceiling numbers but silently drops the shape a run is judged by.
ALTER TABLE execution_report
    ADD COLUMN requested_steps JSON NULL AFTER requested_duration_seconds;
