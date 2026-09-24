BEGIN;

CREATE TABLE refunds (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    payment_id UUID NOT NULL REFERENCES payments (id) ON DELETE RESTRICT,
    user_id UUID NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL UNIQUE,
    request_hash BYTEA NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0),
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'completed', 'failed')),
    provider_reference TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    workflow_id TEXT NOT NULL UNIQUE,
    workflow_started_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE INDEX refunds_payment_created_idx ON refunds (payment_id, created_at DESC);
CREATE INDEX refunds_user_created_idx ON refunds (user_id, created_at DESC);
CREATE INDEX refunds_unstarted_idx ON refunds (created_at, id)
    WHERE workflow_started_at IS NULL AND status = 'pending';

ALTER TABLE ledger_journals
    ADD COLUMN reference_type TEXT,
    ADD COLUMN reference_id UUID;

UPDATE ledger_journals
SET reference_type = 'payment', reference_id = payment_id;

ALTER TABLE ledger_journals
    ALTER COLUMN reference_type SET NOT NULL,
    ALTER COLUMN reference_id SET NOT NULL,
    ADD CONSTRAINT ledger_journals_reference_type_check
        CHECK (reference_type IN ('payment', 'refund')),
    DROP CONSTRAINT ledger_journals_payment_id_key,
    ADD CONSTRAINT ledger_journals_reference_key UNIQUE (reference_type, reference_id);

CREATE INDEX ledger_journals_payment_idx ON ledger_journals (payment_id, created_at);

CREATE TABLE reconciliation_runs (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    workflow_id TEXT NOT NULL UNIQUE,
    requested_by TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'completed', 'failed')),
    issue_count INTEGER NOT NULL DEFAULT 0 CHECK (issue_count >= 0),
    summary JSONB NOT NULL DEFAULT '{}'::jsonb,
    failure_reason TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX reconciliation_runs_created_idx
    ON reconciliation_runs (created_at DESC, id DESC);

CREATE TABLE reconciliation_issues (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    run_id UUID NOT NULL REFERENCES reconciliation_runs (id) ON DELETE CASCADE,
    issue_type TEXT NOT NULL,
    reference_id UUID NOT NULL,
    details JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, issue_type, reference_id)
);

CREATE INDEX reconciliation_issues_run_idx
    ON reconciliation_issues (run_id, issue_type, created_at);

COMMIT;
