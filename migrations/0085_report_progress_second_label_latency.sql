-- 0085_report_progress_second_label_latency: per-label latency tallies per
-- second (phase 103).
--
-- The soak trend phase 99 added to reports is one aggregate for the whole
-- run; a leak in one label is diluted by its healthy siblings' flat
-- latencies, and the aggregate can stay under the leak threshold while a
-- real leak grows. Each label's own trend needs each label's own per-second
-- mean, so the working state a run's seconds keep gains a per-label tally:
-- label_latency is a JSON object mapping label -> {sum, samples}, merged
-- additively across shards the way the label histograms are (a JSON column
-- can only be merged in Go, under the row locks mergeLabels already takes).
--
-- NULL for every row written before phase 103, the 0078 convention: such a
-- second simply carries no per-label observation, and a label's trend is
-- judged only on the seconds that do -- exactly like a second the label did
-- not report in.
ALTER TABLE report_progress_second
    ADD COLUMN label_latency JSON NULL AFTER latency_samples;
