-- 0078_report_progress_second_latency: a second's response-time tally (phase 99).
--
-- The soak trend needs each second's mean response time, so the working
-- state a run's seconds already keep gains the mean's numerator and
-- denominator: latency_sum is Σ(bucket × count) and latency_samples the
-- Σ count across the label rows that landed in the second. Additive
-- defaults keep every pre-phase-99 row exactly as it was -- a zero-sample
-- second carries no mean, and the trend skips it rather than reading it
-- as a zero-latency one.
ALTER TABLE report_progress_second
    ADD COLUMN latency_sum    DOUBLE NOT NULL DEFAULT 0 AFTER label_concurrency,
    ADD COLUMN latency_samples BIGINT NOT NULL DEFAULT 0 AFTER latency_sum;
