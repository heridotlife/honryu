-- 0054_report_progress_label_codes: each HTTP status a label's requests
-- returned, counted while the run is still measuring. JSON like latency,
-- because a map can only be merged in Go under the row lock mergeLabels
-- already takes. NULL -- every row written before this column, or a run whose
-- engine reports no response codes -- reads back as none reported.
ALTER TABLE report_progress_label ADD COLUMN codes JSON NULL;
