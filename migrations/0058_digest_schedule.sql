-- 0058_digest_schedule: the per-project digest firing schedule (phase 42).
-- One row per project is the whole configuration: which period's digest
-- fires, whether it is on, and when the scheduler last claimed a fire --
-- deliberately NOT one row per future fire time the way execution schedules
-- reserve occurrences, because a digest claims nothing: it is a read of
-- already-stored reports plus a best-effort delivery, so there is no
-- capacity to reserve ahead of time and no horizon to extend. last_fired
-- doubles as the concurrency guard -- the due claim is an UPDATE whose
-- WHERE re-checks dueness (last_fired still old enough), so two scheduler
-- replicas cannot double-fire the same window, mirroring ClaimDue's
-- affected-rows pattern. last_fired survives a period switch on purpose:
-- the new period's fire times tile from it, and its first window still
-- starts at the last same-period digest's window_end (or one full period
-- back when there is none), never overlapping what was already sent.
CREATE TABLE IF NOT EXISTS digest_schedule (
    project_id INT UNSIGNED NOT NULL PRIMARY KEY,
    period     VARCHAR(16)  NOT NULL,
    enabled    TINYINT(1)   NOT NULL DEFAULT 1,
    last_fired TIMESTAMP    NULL,
    KEY idx_digest_schedule_enabled (enabled)
) CHARSET=utf8mb4;
