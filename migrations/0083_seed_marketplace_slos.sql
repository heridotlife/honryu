-- 0083_seed_marketplace_slos: marketplace simulation seed, part 5 (phase 102).
-- Full-wipe context: DB was dropped & re-migrated from 0001 right before
-- these run; INSERT ... SELECT with NOT EXISTS guards = idempotent.
-- Project SLOs
INSERT INTO project_slo (project_id, name, target_p95_ms, target_error_rate, target_success_ratio)
SELECT p.id, v.slo, v.p95, v.er, v.sr
FROM (
    SELECT 'mp-checkout' AS pname, 'checkout-slo' AS slo, 300.0 AS p95, 0.01 AS er, 0.99 AS sr
    UNION ALL SELECT 'mp-search', 'search-slo', 800.0, 0.02, 0.98
    UNION ALL SELECT 'mp-items', 'items-slo', 500.0, 0.01, 0.99
    UNION ALL SELECT 'mp-cart', 'cart-slo', 350.0, 0.01, 0.99
    UNION ALL SELECT 'pm-supersale', 'supersale-slo', 250.0, 0.005, 0.995
    UNION ALL SELECT 'labs-httpbin-qa', 'qa-slo', 1000.0, 0.05, 0.95
    UNION ALL SELECT 'sup-fulfillment', 'fulfillment-slo', 750.0, 0.02, 0.98
    UNION ALL SELECT 'sup-inventory', 'inventory-slo', 600.0, 0.02, 0.98
) AS v
JOIN project p ON p.name = v.pname
WHERE NOT EXISTS (
    SELECT 1 FROM project_slo x WHERE x.project_id = p.id AND x.name = v.slo
);
