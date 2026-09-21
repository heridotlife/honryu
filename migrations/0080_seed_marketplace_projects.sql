-- 0080_seed_marketplace_projects: marketplace simulation seed, part 2 (phase 102).
-- Full-wipe context: DB was dropped & re-migrated from 0001 right before
-- these run; INSERT ... SELECT with NOT EXISTS guards = idempotent.
-- Projects
INSERT INTO project (name, owner, sid, tenant_id)
SELECT v.name, 'honryu', v.sid, t.id
FROM (
    SELECT 'mp-checkout' AS name, 'ckout' AS sid, 'marketplace' AS tenant
    UNION ALL SELECT 'mp-search',      'srch',  'marketplace'
    UNION ALL SELECT 'mp-items',       'items', 'marketplace'
    UNION ALL SELECT 'mp-cart',        'cart',  'marketplace'
    UNION ALL SELECT 'pm-supersale',   'sprsl', 'marketplace'
    UNION ALL SELECT 'labs-httpbin-qa','labs',  'marketplace-labs'
    UNION ALL SELECT 'sup-fulfillment','fulfl', 'supplier'
    UNION ALL SELECT 'sup-inventory',  'invntr','supplier'
) AS v
JOIN tenant t ON t.name = v.tenant
WHERE NOT EXISTS (SELECT 1 FROM project p WHERE p.name = v.name);
