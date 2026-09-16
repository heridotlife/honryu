-- 0068_threshold_results: one run's report graded against its scenario's
-- thresholds (phase 72), one row per threshold evaluated. Written when the
-- run's report is finalised -- the one shared exit every finalisation path
-- reaches -- and read back on the run's report surface.
--
-- The definition rides along as a snapshot (metric, comparison, value): a
-- result is historical evidence, and stays legible even after the operator
-- re-edits the scenario's thresholds. The threshold_id FK cascades, so the
-- rows of a threshold the replace-all removed die with it -- its criterion
-- no longer exists -- while unchanged definitions keep their row identity
-- across re-saves and their results survive.
--
-- run_id references the report itself (execution_report is one row per run),
-- so results are per run and a re-run of the same execution writes its own
-- rows without touching an earlier run's. Both key columns cascade.
--
-- observed_value and satisfied are deliberately nullable, with reason beside
-- them: a metric the report carries no figure for (a percentile the engine
-- never measured) is unknown -- satisfied NULL plus a reason -- never a
-- fabricated fail.
CREATE TABLE IF NOT EXISTS threshold_results (
    id             INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    execution_id   INT UNSIGNED NOT NULL,
    run_id         INT UNSIGNED NOT NULL,
    threshold_id   INT UNSIGNED NOT NULL,
    metric         VARCHAR(32)  NOT NULL,
    comparison     VARCHAR(8)   NOT NULL,
    value          DOUBLE       NOT NULL,
    observed_value DOUBLE       NULL,
    satisfied      TINYINT(1)   NULL,
    reason         VARCHAR(255) NULL,
    evaluated_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uq_threshold_results_run (run_id, threshold_id),
    KEY idx_threshold_results_execution (execution_id),
    CONSTRAINT fk_threshold_results_execution
        FOREIGN KEY (execution_id) REFERENCES execution (id) ON DELETE CASCADE,
    CONSTRAINT fk_threshold_results_report
        FOREIGN KEY (run_id) REFERENCES execution_report (run_id) ON DELETE CASCADE,
    CONSTRAINT fk_threshold_results_threshold
        FOREIGN KEY (threshold_id) REFERENCES scenario_thresholds (id) ON DELETE CASCADE
) CHARSET=utf8mb4;
