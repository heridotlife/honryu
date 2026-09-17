-- 0070_scenario_versions_backfill: every existing scenario's pre-versioning
-- state becomes its version 1 (phase 80), so history starts complete
-- instead of at each row's first post-0069 edit. The snapshot is assembled
-- with the exact field names the Go ScenarioSnapshot type unmarshals:
-- the scenario row's own columns plus the file records (test_file, data)
-- and the stored requests fragment, joined in.
--
-- created_time renders as RFC3339 (the JSON encoding time.Time uses);
-- second precision is what the TIMESTAMP column carries. tenant_id NULL
-- stays JSON null; is_template forces a REAL JSON boolean (CAST('true' AS
-- JSON) -- TRUE/FALSE literals are integers in MySQL and would serialize
-- as 0/1); data is always an array (COALESCE over the aggregate, empty
-- when the scenario has none).
--
-- WHERE NOT EXISTS keeps a re-apply a no-op, the same idempotency 0065's
-- backfill documents: scenarios already carrying a version 1 are never
-- rewritten.
INSERT INTO scenario_versions (scenario_id, version, snapshot, created_by)
SELECT s.id, 1, JSON_OBJECT(
         'id', s.id,
         'name', s.name,
         'project_id', s.project_id,
         'kind', s.kind,
         'engine', s.engine,
         'tenant_id', s.tenant_id,
         'created_by', s.created_by,
         'updated_by', s.updated_by,
         'created_time', DATE_FORMAT(s.created_time, '%Y-%m-%dT%H:%i:%sZ'),
         'is_template', CASE WHEN s.is_template THEN CAST('true' AS JSON) ELSE CAST('false' AS JSON) END,
         'template_name', s.template_name,
         'test_file', tf.filename,
         'data', COALESCE(d.names, JSON_ARRAY()),
         'requests', sr.raw
       ), s.created_by
FROM scenario s
LEFT JOIN scenario_test_file tf ON tf.scenario_id = s.id
LEFT JOIN (
    SELECT scenario_id, JSON_ARRAYAGG(filename) AS names
    FROM scenario_data
    GROUP BY scenario_id
) d ON d.scenario_id = s.id
LEFT JOIN scenario_requests sr ON sr.scenario_id = s.id
WHERE NOT EXISTS (
    SELECT 1 FROM scenario_versions sv WHERE sv.scenario_id = s.id
);
