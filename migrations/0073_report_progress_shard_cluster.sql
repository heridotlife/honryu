-- 0073_report_progress_shard_cluster: key a shard's working state by its load
-- origin too (phase 88 fan-out).
--
-- The same collision 0025 fixed for scenarios, fan-out fixes for clusters:
-- under full shard duplication every target cluster runs a pod with the SAME
-- (scenario, shard) indexes. Without cluster in the key, the clusters' pods
-- share one row: each push looks like the other cluster's pod restarting (the
-- stream changes, the watermark resets -- intervals still count, by luck of
-- the reset-on-stream-change path) but the finished flag is ONE bit for many
-- pods, so the first cluster to finish marks the shard done and the run can
-- finalise while other clusters are still loading -- a truncated fan-out run
-- reported as complete.
--
-- '' is the deployment default cluster, so every ordinary run's rows keep
-- their exact old identity and behaviour.
ALTER TABLE report_progress_shard
    ADD COLUMN cluster VARCHAR(100) NOT NULL DEFAULT '' AFTER scenario_id,
    DROP PRIMARY KEY,
    ADD PRIMARY KEY (run_id, scenario_id, cluster, shard_index);
