-- 0067_scenario_thresholds: a scenario's k6-style pass/fail bounds (phase
-- 72). Each row names one metric (http_p95_ms, http_p99_ms, error_rate,
-- throughput_qps -- the enum lives app-side, where the operator's input
-- arrives, so the rule and its message live in one place), a direction
-- (lt = ceiling, gt = floor), and the bound in the metric's own unit. A run
-- is graded against its scenario's set when its report lands; the verdict
-- stays the engine's own -- thresholds are additive evidence, never the run
-- outcome.
--
-- The (scenario_id, metric, comparison, value) unique key is the storage
-- identity of a distinct bound: the use-case's replace-all diff matches on
-- exactly it, so unchanged rows keep their id (and their recorded results)
-- across an idempotent re-save. scenario_id cascades -- a deleted scenario's
-- thresholds mean nothing.
CREATE TABLE IF NOT EXISTS scenario_thresholds (
    id           INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    scenario_id  INT UNSIGNED NOT NULL,
    metric       VARCHAR(32)  NOT NULL,
    comparison   VARCHAR(8)   NOT NULL,
    value        DOUBLE       NOT NULL,
    created_time TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uq_scenario_thresholds (scenario_id, metric, comparison, value),
    CONSTRAINT fk_scenario_thresholds_scenario
        FOREIGN KEY (scenario_id) REFERENCES scenario (id) ON DELETE CASCADE
) CHARSET=utf8mb4;
