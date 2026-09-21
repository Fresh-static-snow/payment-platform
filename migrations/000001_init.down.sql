BEGIN;

DROP TABLE IF EXISTS notification_sends;
DROP TABLE IF EXISTS notification_deliveries;
DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS ledger_journals;
DROP TABLE IF EXISTS risk_decisions;
DROP TABLE IF EXISTS receipts;
DROP TABLE IF EXISTS receipt_requests;
DROP TABLE IF EXISTS consumer_inbox;
DROP TABLE IF EXISTS outbox_events;
DROP TABLE IF EXISTS payments;

COMMIT;
