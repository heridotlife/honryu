-- 0065_calibration_job_scenario_backfill: resolve 0064's legacy NULLs
-- (phase 67a).
--
-- Every calibration_job's execution is a CalibrateEngine execution, and
-- calibrationapp.Create binds each of those to exactly one scenario (its
-- load profile stores a single loadprofile.Entry; writeProfile and
-- runClassifiedStep both fail on anything else) -- so the join through
-- execution_scenario yields exactly one scenario_id per job's execution,
-- and this UPDATE assigns each legacy row its true scenario. Ordinary
-- executions may bind several scenarios; calibration ones cannot, which is
-- what makes a straight JOIN safe here.
--
-- WHERE scenario_id IS NULL keeps a re-apply a no-op, the same idempotency
-- 0062/0063's INSERT IGNORE buys -- rows already carrying a scenario
-- (nothing yet, but also nothing after) are never rewritten.
UPDATE calibration_job cj
    JOIN execution_scenario es ON es.execution_id = cj.execution_id
    SET cj.scenario_id = es.scenario_id
    WHERE cj.scenario_id IS NULL;
