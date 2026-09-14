-- 0063_seed_template_requests: the workload fragments for 0062's templates
-- (phase 65).
--
-- Each row is the bare taurus.Scenario fragment the editor round-trips (NOT
-- wrapped in a scenarios: map -- the wrapped shape leaves requests empty and
-- fails validation), the same shape the NewTest flow writes via
-- PUT /api/scenarios/{id}/requests. Joined by template_name, not id, so the
-- statement is independent of AUTO_INCREMENT state.
--
-- INSERT IGNORE for the same reason as 0062: scenario_requests' primary key
-- is scenario_id, so a re-apply is a no-op instead of a duplicate-key error.
INSERT IGNORE INTO scenario_requests (scenario_id, raw)
SELECT id,
'default-address: https://httpbin.org
requests:
    - method: GET
      url: /get'
FROM scenario WHERE template_name = 'httpbin-baseline'
UNION ALL
SELECT id,
'default-address: https://httpbin.org
timeout: 10s
requests:
    - method: GET
      url: /delay/1'
FROM scenario WHERE template_name = 'httpbin-delay'
UNION ALL
SELECT id,
'default-address: https://httpbin.org
requests:
    - method: GET
      url: /get
    - method: POST
      url: /post
      body:
        source: honryu-spike-template'
FROM scenario WHERE template_name = 'httpbin-spike';
