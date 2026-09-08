-- 0055_run_share: a token-gated public mirror of one run's report. A share
-- link is a bare capability: knowing the 256-bit token is the entire
-- authorization, so GET /api/share/{token} sits outside the auth middleware
-- and serves exactly the run report payload -- nothing else, no execution
-- listing, no shard objects. Tokens are never derived from anything (32
-- crypto/rand bytes, hex), so one customer's link cannot be guessed from
-- another's.
--
-- Multiple live tokens per run are allowed: sharing a report with two
-- customers and revoking one must not break the other, which is why the
-- unique key is the token, not the run. Expiry is fetch-time policy, not
-- storage state: an expired row stays (the issuing UI keeps listing it)
-- while the public fetch answers 404.
CREATE TABLE IF NOT EXISTS run_share (
    id           INT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    run_id       INT UNSIGNED NOT NULL,
    token        CHAR(64)     NOT NULL,
    created_by   VARCHAR(255) NULL,
    created_time TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_time DATETIME     NULL,
    UNIQUE KEY uk_run_share_token (token),
    KEY idx_run_share_run (run_id)
) CHARSET=utf8mb4;
