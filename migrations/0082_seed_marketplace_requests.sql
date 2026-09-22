-- 0082_seed_marketplace_requests: marketplace simulation seed, part 4 (phase 102).
-- Full-wipe context: DB was dropped & re-migrated from 0001 right before
-- these run; INSERT ... SELECT with NOT EXISTS guards = idempotent.
-- Scenario request fragments
INSERT INTO scenario_requests (scenario_id, raw)
SELECT s.id, v.raw
FROM (
    SELECT 'checkout-pay-json' AS sname, 'default-address: https://httpbin.pve.heri.life
requests:
    - method: POST
      url: /post
      headers:
        Content-Type: application/json
      body:
        order_id: ${UUID}
        sku: MP-1001
        qty: 2
        channel: web' AS raw
    UNION ALL SELECT 'checkout-pay-form', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: POST
      url: /forms/post
      headers:
        Content-Type: application/x-www-form-urlencoded
      body:
        custname: alice
        size: large
        payment: cod' 
    UNION ALL SELECT 'checkout-session-bearer', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /bearer
      headers:
        Authorization: Bearer mp-checkout-session-token'
    UNION ALL SELECT 'checkout-update-digest', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: PUT
      url: /anything/checkout/order
      headers:
        Content-Type: application/json
      body:
        status: CONFIRMED'
    UNION ALL SELECT 'search-query-slow', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /delay/1?q=laptop&page=2
      headers:
        Accept: application/json'
    UNION ALL SELECT 'search-compressed', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /gzip
      headers:
        Accept-Encoding: gzip'
    UNION ALL SELECT 'search-session-uuid', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /uuid'
    UNION ALL SELECT 'items-browse-page', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /get?page=1&sort=price_asc&category=electronics'
    UNION ALL SELECT 'items-image-cdn', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /image/png'
    UNION ALL SELECT 'items-detail-variants', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /anything/items/MP-1001/variants'
    UNION ALL SELECT 'cart-add-item', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: POST
      url: /anything/cart/items
      headers:
        Content-Type: application/json
      body:
        sku: MP-2049
        qty: 1'
    UNION ALL SELECT 'cart-update-qty', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: PATCH
      url: /anything/cart/items/MP-2049
      headers:
        Content-Type: application/json
      body:
        qty: 3'
    UNION ALL SELECT 'cart-remove-item', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: DELETE
      url: /anything/cart/items/MP-2049'
    UNION ALL SELECT 'supersale-flash-hit', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /get?campaign=supersale&slot=flash'
    UNION ALL SELECT 'supersale-deep-stream', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /stream/10'
    UNION ALL SELECT 'supersale-error-budget', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /status/503'
    UNION ALL SELECT 'qa-methods-matrix', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /get
    - method: POST
      url: /post
      body: qa=methods
    - method: PUT
      url: /put
      body: qa=methods
    - method: PATCH
      url: /patch
      body: qa=methods
    - method: DELETE
      url: /delete'
    UNION ALL SELECT 'qa-auth-matrix', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /bearer
      headers:
        Authorization: Bearer qa-token-123
    - method: GET
      url: /basic-auth/qauser/qapass
      headers:
        Authorization: Basic cWF1c2VyOnFhcGFzcw=='
    UNION ALL SELECT 'qa-behavior-matrix', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /redirect/3
    - method: GET
      url: /bytes/512
    - method: GET
      url: /deflate'
    UNION ALL SELECT 'fulfill-status-poll', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /anything/fulfillment/orders/8891/status'
    UNION ALL SELECT 'fulfill-redirect-walk', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /redirect/5'
    UNION ALL SELECT 'inventory-sync-push', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: PUT
      url: /anything/inventory/sku/MP-5521
      headers:
        Content-Type: application/json
      body:
        on_hand: 42
        reserved: 7'
    UNION ALL SELECT 'inventory-snapshot-pull', 'default-address: https://httpbin.pve.heri.life
requests:
    - method: GET
      url: /json'
) AS v
JOIN scenario s ON s.name = v.sname
WHERE NOT EXISTS (
    SELECT 1 FROM scenario_requests r WHERE r.scenario_id = s.id
);
