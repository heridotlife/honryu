-- 0064_calibration_job_scenario: the scenario a calibration job calibrates
-- (phase 67a).
--
-- calibration_job recorded only execution_id, so answering "which scenario
-- did this job measure" meant joining the execution's load profile on every
-- read. The scenario is now a column -- the scenario-first API reads it
-- directly (the trigger response's own lineage), and legacy rows are
-- backfilled in 0065 from the same join.
--
-- NULLABLE, deliberately: a job created before this column existed and not
-- covered by the backfill (a calibration whose execution lost its scenario
-- binding) stays NULL rather than lying with a guessed 0; ports'
-- CalibrationJob.ScenarioID reads NULL back as 0, "unknown", the same
-- convention execution_scenario's optional sides already follow.
--
-- Index, no FK constraint, exactly like 0036's execution_id: the codebase
-- has zero FK constraints, and a real one would be refused anyway --
-- scenario.id is INT UNSIGNED while calibration_job.id/execution_id are
-- BIGINT UNSIGNED, and MySQL requires matching integer types for FKs.
ALTER TABLE calibration_job
    ADD COLUMN scenario_id BIGINT UNSIGNED NULL,
    ADD INDEX idx_calibration_job_scenario (scenario_id);
