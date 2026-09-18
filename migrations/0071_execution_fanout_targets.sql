-- 0071_execution_fanout_targets: the clusters a fan-out execution runs its
-- full load profile on, simultaneously (phase 88).
--
-- Added as a column rather than folded into 0003_execution.sql: migrations
-- are tracked by filename, so editing an applied file leaves every database
-- that already recorded it without the change.
--
-- JSON array of clusterregistry.Cluster names, NULL = an ordinary
-- single-cluster execution (the cluster column), non-empty array = fan-out.
-- The default cluster never appears inside it: choosing the default is what
-- leaving fan-out unset means, and the empty-string name would collide with
-- the sentinel convention every other cluster-bearing column uses.
ALTER TABLE execution
    ADD COLUMN fanout_targets JSON NULL;
