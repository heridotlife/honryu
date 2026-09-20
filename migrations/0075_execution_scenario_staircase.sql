-- 0075_execution_scenario_staircase: staircase load shapes (phase 98).
--
-- Two changes, both on execution_scenario:
--
--   mode   widened VARCHAR(8) -> VARCHAR(16): phase 90 sized the column
--          for "burst"/"ramp"/"soak" (max 4) and "staircase" (9) does
--          not fit -- a mode name that outgrows its column is a 500 on
--          every config PUT, not a validation error.
--   steps  the staircase's step count (2-10, resolved default 5), 0 =
--          not a staircase. Load-bearing downstream, unlike mode: a
--          staircase's whole window is steps consecutive per-step holds,
--          and its deploy expands the entry into that many sequential
--          execution blocks per engine pod.
--
-- Like 0074, added as new ALTERs rather than edits: applied migrations
-- are tracked by filename and never rewritten.
ALTER TABLE execution_scenario
    MODIFY COLUMN mode VARCHAR(16) NULL,
    ADD COLUMN steps INT NOT NULL DEFAULT 0;
