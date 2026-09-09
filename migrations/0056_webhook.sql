-- 0056_webhook: per-project webhook endpoints notified when a run
-- completes (phase 38). A webhook is a receiver Honryu POSTs a
-- run.completed event to -- Slack, Discord, or any generic receiver --
-- which is why the URL is https-only and carries an optional secret: when
-- set, deliveries are signed (HMAC-SHA256 over the raw body,
-- X-Honryu-Signature) so the receiver can prove the call came from this
-- deployment and was not tampered with in transit.
--
-- Registration is per project (index on project_id), matching how the rest
-- of the surface scopes: every completed run of an execution in the
-- project notifies each enabled endpoint. Delivery is best-effort with
-- bounded retries and never affects the run itself -- Honryu delivers a
-- notification, it does not become the receiver's mail server (the same
-- propagates-not-traces law the correlation id follows). enabled is
-- storage state (unlike a share link's expiry, which is fetch-time
-- policy): a paused webhook is skipped at delivery time by not being
-- listed, so pause/resume is a plain UPDATE.
CREATE TABLE IF NOT EXISTS webhook (
    id           INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    project_id   INT UNSIGNED NOT NULL,
    url          VARCHAR(2048) NOT NULL,
    secret       VARCHAR(128)  NULL,
    created_by   VARCHAR(255)  NULL,
    created_time TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    enabled      TINYINT(1)    NOT NULL DEFAULT 1,
    KEY idx_webhook_project (project_id)
) CHARSET=utf8mb4;
