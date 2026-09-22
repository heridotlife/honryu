-- 0086_default_tenant_quota: every existing tenant gets an explicit default
-- quota row on cluster 'home' (phase 104). Belt for existing installs: the
-- read path already substitutes the platform default (reservation.DefaultCeiling,
-- 10 engine units) whenever no row exists, so this row changes nothing about
-- admission -- it makes the default visible and editable in the ledger the
-- admin surface reads, instead of implicit. Single statement, idempotent
-- (INSERT ... SELECT ... WHERE NOT EXISTS, the 0079-0084 pattern).
INSERT INTO tenant_quota (tenant_id, cluster, ceiling)
SELECT t.id, 'home', 10
FROM tenant t
WHERE NOT EXISTS (
    SELECT 1 FROM tenant_quota q
    WHERE q.tenant_id = t.id AND q.cluster = 'home'
);
