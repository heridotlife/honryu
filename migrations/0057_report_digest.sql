-- 0057_report_digest: the stored rows behind per-project periodic report
-- digests (phase 42). A digest is the aggregation of every completed run a
-- project's executions produced in one window -- counts by outcome, threshold
-- failures, and a per-execution roll-up -- serialised once, at fire time, as
-- the exact payload delivered to the project's webhooks (event
-- report.digest). Storing the serialised bytes (rather than recomputing from
-- the reports table on read) is the point: the in-app feed and every webhook
-- receiver see the same numbers for the same window forever, even after the
-- underlying runs are old news or the aggregation code has moved on.
--
-- window_end of the latest row per (project_id, period) is also the
-- digest use-case's own bookmark: the next digest of that period starts
-- exactly there, so windows neither overlap nor leave gaps. created_time is
-- the database's to assign; it orders the operator's recent-digests feed
-- alongside id.
CREATE TABLE IF NOT EXISTS report_digest (
    id           INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    project_id   INT UNSIGNED NOT NULL,
    period       VARCHAR(16)  NOT NULL,
    window_start TIMESTAMP    NOT NULL,
    window_end   TIMESTAMP    NOT NULL,
    payload      TEXT         NOT NULL,
    created_time TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    KEY idx_report_digest_project (project_id)
) CHARSET=utf8mb4;
