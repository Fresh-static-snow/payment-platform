BEGIN;

ALTER TABLE reconciliation_runs
    DROP CONSTRAINT reconciliation_runs_finished_at_check,
    DROP CONSTRAINT reconciliation_runs_requester_check;

ALTER TABLE ledger_journals
    DROP CONSTRAINT ledger_journals_payment_reference_check;

-- This intentionally fails if different users have since reused the same key;
-- callers must resolve those collisions before restoring the legacy global
-- uniqueness rule.
ALTER TABLE refunds
    DROP CONSTRAINT refunds_terminal_details_check,
    DROP CONSTRAINT refunds_request_hash_sha256_check,
    DROP CONSTRAINT refunds_user_idempotency_key_key,
    ADD CONSTRAINT refunds_idempotency_key_key UNIQUE (idempotency_key);

COMMIT;
