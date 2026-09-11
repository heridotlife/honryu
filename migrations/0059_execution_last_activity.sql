-- 0059_execution_last_activity: the engine idle clock the activity-aware TTL
-- reaper measures (phase 46). Every lifecycle event that proves an execution's
-- engines are in use -- a deploy, a run start, a run completion (natural
-- finalize OR a stop), a calibration step -- restamps it, so an execution that
-- finishes naturally keeps its engines warm exactly TTL for fast re-runs and
-- then the scheduler-side reaper tears the pods down. NULL (the default) means
-- "never stamped": an execution deployed before this column existed, whose
-- pods may have been idle for days -- today's orphan problem -- so the reaper
-- falls back to the StatefulSet's own age rather than skipping it. No index:
-- the reaper never range-scans this column; it reads it per candidate by
-- primary key, candidates coming from the cluster's deployed set.
ALTER TABLE execution
    ADD COLUMN last_activity_at TIMESTAMP NULL;
