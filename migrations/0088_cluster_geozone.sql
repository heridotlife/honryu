-- 0088_cluster_geozone: display-only geo/zone labels on registered clusters
-- (phase 109, k6-parity gap #4). A fan-out picker that shows bare cluster
-- names gives no sense of WHERE each target runs; geo ("eu") and zone
-- ("eu-central-1") let a user reason about latency and geographic spread,
-- and read them back on the registry page. The labels are never scheduling
-- inputs -- placement stays name-driven -- and empty string means unlabeled:
-- NOT NULL DEFAULT '' keeps every existing row and every pre-phase-109
-- read/write path valid without code changes.
ALTER TABLE cluster_registry
    ADD COLUMN geo VARCHAR(64) NOT NULL DEFAULT '' AFTER origin,
    ADD COLUMN zone VARCHAR(64) NOT NULL DEFAULT '' AFTER geo;
