-- 0060_digest_delivery_status: the delivery outcome on a stored digest
-- (phase 60). Until now the fire's delivery result lived only in a log
-- line: the row could not say whether the report.digest ever left the
-- building, and since digests tile the timeline -- never re-fired, never
-- re-sent -- a silently lost window was unrecoverable and invisible.
-- delivery_status carries the finalize point's verdict: 'pending' (the
-- column default, covering rows fired with no deliverer wired, fires with
-- nothing configured to notify, and every pre-0060 row -- "no confirmed
-- delivery recorded", not an error), 'delivered' (at least one configured
-- receiver confirmed), 'failed' (delivery attempted, none confirmed).
-- delivered_at is the confirmation stamp, NULL for everything that is not
-- delivered -- a failed delivery confirmed nothing, and pending has not
-- happened yet. No backfill: the honest value for a historical row is
-- pending, and no index -- the feed reads rows by project already and the
-- status rides along by primary key.
ALTER TABLE report_digest
    ADD COLUMN delivery_status VARCHAR(16) NOT NULL DEFAULT 'pending',
    ADD COLUMN delivered_at TIMESTAMP NULL;
