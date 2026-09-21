-- 0079_seed_marketplace_tenants: marketplace simulation seed, part 1 (phase 102).
-- Full-wipe context: DB was dropped & re-migrated from 0001 right before
-- these run; INSERT ... SELECT with NOT EXISTS guards = idempotent.
-- Tenants
INSERT INTO tenant (name, display_name)
SELECT v.name, v.dn FROM (
    SELECT 'marketplace' AS name, 'Marketplace' AS dn
    UNION ALL SELECT 'marketplace-labs', 'Marketplace Labs'
    UNION ALL SELECT 'supplier', 'Supplier Partners'
) AS v
WHERE NOT EXISTS (SELECT 1 FROM tenant t WHERE t.name = v.name);
