-- 0066_project_slo: per-project service-level objectives and the targets an
-- operator holds their service to (phase 68). An SLO is a named set of at
-- least one target -- p95 response time (milliseconds, the unit operators
-- quote, though reports store seconds), error rate, and success ratio -- that
-- the budget endpoint grades against the run reports a time window holds.
--
-- The three target columns are deliberately nullable: an SLO may pin any
-- subset of the three, and "at least one" is enforced where the operator's
-- input arrives (the domain's Validate) rather than in DDL, so the rule and
-- its error message live in exactly one place and older MySQL versions that
-- ignore CHECK constraints cannot silently accept an empty SLO.
--
-- Scoping is by project everywhere (matching how the rest of the surface
-- works: a webhook, a digest, an SLO is only ever read or deleted through the
-- project it belongs to), and the (project_id, name) unique key is the
-- human-facing identity: two SLOs with the same name in one project would
-- make every digest line and dashboard badge ambiguous. The leftmost column
-- also serves the by-project list, so no separate project index is needed.
CREATE TABLE IF NOT EXISTS project_slo (
    id                   INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    project_id           INT UNSIGNED NOT NULL,
    name                 VARCHAR(128) NOT NULL,
    target_p95_ms        DOUBLE       NULL,
    target_error_rate    DOUBLE       NULL,
    target_success_ratio DOUBLE       NULL,
    created_time         TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE KEY uq_project_slo_name (project_id, name)
) CHARSET=utf8mb4;
