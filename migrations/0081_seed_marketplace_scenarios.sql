-- 0081_seed_marketplace_scenarios: marketplace simulation seed, part 3 (phase 102).
-- Full-wipe context: DB was dropped & re-migrated from 0001 right before
-- these run; INSERT ... SELECT with NOT EXISTS guards = idempotent.
-- Scenarios
INSERT INTO scenario (name, project_id, tenant_id, created_by, kind, engine)
SELECT v.sname, p.id, p.tenant_id, 'honryu', 'portable', ''
FROM (
    
    SELECT 'mp-checkout' AS pname, 'checkout-pay-json' AS sname UNION ALL
    SELECT 'mp-checkout', 'checkout-pay-form' UNION ALL
    SELECT 'mp-checkout', 'checkout-session-bearer' UNION ALL
    SELECT 'mp-checkout', 'checkout-update-digest'
    
    UNION ALL SELECT 'mp-search', 'search-query-slow' UNION ALL
    SELECT 'mp-search', 'search-compressed' UNION ALL
    SELECT 'mp-search', 'search-session-uuid'
    
    UNION ALL SELECT 'mp-items', 'items-browse-page' UNION ALL
    SELECT 'mp-items', 'items-image-cdn' UNION ALL
    SELECT 'mp-items', 'items-detail-variants'
    
    UNION ALL SELECT 'mp-cart', 'cart-add-item' UNION ALL
    SELECT 'mp-cart', 'cart-update-qty' UNION ALL
    SELECT 'mp-cart', 'cart-remove-item'
    
    UNION ALL SELECT 'pm-supersale', 'supersale-flash-hit' UNION ALL
    SELECT 'pm-supersale', 'supersale-deep-stream' UNION ALL
    SELECT 'pm-supersale', 'supersale-error-budget'
    
    UNION ALL SELECT 'labs-httpbin-qa', 'qa-methods-matrix' UNION ALL
    SELECT 'labs-httpbin-qa', 'qa-auth-matrix' UNION ALL
    SELECT 'labs-httpbin-qa', 'qa-behavior-matrix'
    
    UNION ALL SELECT 'sup-fulfillment', 'fulfill-status-poll' UNION ALL
    SELECT 'sup-fulfillment', 'fulfill-redirect-walk'
    
    UNION ALL SELECT 'sup-inventory', 'inventory-sync-push' UNION ALL
    SELECT 'sup-inventory', 'inventory-snapshot-pull'
) AS v
JOIN project p ON p.name = v.pname
WHERE NOT EXISTS (SELECT 1 FROM scenario s WHERE s.name = v.sname AND s.project_id = p.id);
