-- 0074_execution_scenario_mode: simplified execution modes (phase 90).
--
-- Added as a column rather than folded into 0003_execution.sql: migrations
-- are tracked by filename, so editing an applied file leaves every database
-- that already recorded it without the change.
--
-- Provenance only: which simplified mode ("burst"/"ramp"/"soak") the entry
-- was stated as before the server resolved it into ordinary concurrency/
-- engines/ramp-up numbers. NULL = an advanced entry (the only kind before
-- phase 90); the stored row is always fully resolved, so nothing downstream
-- of the repository ever reads this to derive load.
ALTER TABLE execution_scenario
    ADD COLUMN mode VARCHAR(8) NULL;
