-- 0084_seed_supersale_campaign: marketplace simulation seed, part 6 (phase 102).
-- Full-wipe context: DB was dropped & re-migrated from 0001 right before
-- these run; INSERT ... SELECT with NOT EXISTS guards = idempotent.
-- Supersale campaign shell (15-day window, no designated executions)
INSERT INTO campaign (name, tenant_id, window_start, window_end)
SELECT 'Supersale Readiness', t.id, NOW(), DATE_ADD(NOW(), INTERVAL 15 DAY)
FROM tenant t
WHERE t.name = 'marketplace'
  AND NOT EXISTS (SELECT 1 FROM campaign c WHERE c.name = 'Supersale Readiness');
