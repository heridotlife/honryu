-- 0069_scenario_versions: the scenario edit audit trail (phase 80), one
-- row per captured state. Every scenario write records the CURRENT state
-- here (version = max+1) BEFORE applying the change; creation captures
-- version 1. History is append-only: no path rewrites or deletes a version
-- row (restore captures the pre-restore state as a new version -- restore
-- is itself a version). The FK cascades, so a deleted scenario's history
-- dies with it.
--
-- snapshot is the full scenario shape as JSON: the scenario row's own
-- columns plus its file records (test_file, data -- names, not blobs) and
-- its stored requests fragment. 0070 backfills one row per existing
-- scenario.
--
-- created_by is the principal that produced the change, when the request
-- carried one (RBAC mode); NULL when none was reachable (legacy no-auth
-- mode). The UNIQUE key makes "version = max+1 per scenario" a storage
-- invariant, not just an application promise.
CREATE TABLE IF NOT EXISTS scenario_versions (
    id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    scenario_id  INT UNSIGNED    NOT NULL,
    version      INT UNSIGNED    NOT NULL,
    snapshot     JSON            NOT NULL,
    created_time TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by   VARCHAR(255)    NULL,
    UNIQUE KEY uq_scenario_versions_scenario_version (scenario_id, version),
    CONSTRAINT fk_scenario_versions_scenario
        FOREIGN KEY (scenario_id) REFERENCES scenario (id) ON DELETE CASCADE
) CHARSET=utf8mb4;
