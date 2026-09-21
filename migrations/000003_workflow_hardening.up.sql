BEGIN;

-- Idempotency keys are scoped to the authenticated owner. This prevents one
-- tenant from reserving a key for every other tenant while retaining a single
-- database arbiter for concurrent retries by the same owner.
ALTER TABLE refunds
    DROP CONSTRAINT refunds_idempotency_key_key,
    ADD CONSTRAINT refunds_user_idempotency_key_key UNIQUE (user_id, idempotency_key),
    ADD CONSTRAINT refunds_request_hash_sha256_check
        CHECK (octet_length(request_hash) = 32),
    ADD CONSTRAINT refunds_terminal_details_check CHECK (
        (status IN ('pending', 'processing') AND provider_reference = '' AND failure_reason = '')
        OR (status = 'completed' AND length(btrim(provider_reference)) > 0 AND failure_reason = '')
        OR (status = 'failed' AND provider_reference = '' AND length(btrim(failure_reason)) > 0)
    );

-- Payment journals reference the payment itself. Refund journals reference a
-- refund while retaining the original payment in payment_id.
ALTER TABLE ledger_journals
    ADD CONSTRAINT ledger_journals_payment_reference_check CHECK (
        reference_type <> 'payment' OR reference_id = payment_id
    );

ALTER TABLE reconciliation_runs
    ADD CONSTRAINT reconciliation_runs_requester_check
        CHECK (length(btrim(requested_by)) > 0),
    ADD CONSTRAINT reconciliation_runs_finished_at_check CHECK (
        (status IN ('pending', 'running') AND finished_at IS NULL)
        OR (status IN ('completed', 'failed') AND finished_at IS NOT NULL)
    );

COMMIT;
