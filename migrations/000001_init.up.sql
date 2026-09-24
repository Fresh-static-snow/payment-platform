BEGIN;

CREATE TABLE payments (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    idempotency_key VARCHAR(255) NOT NULL UNIQUE,
    request_hash BYTEA NOT NULL,
    user_id UUID NOT NULL,
    amount BIGINT NOT NULL CHECK (amount > 0),
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    description VARCHAR(500) NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('pending', 'processing', 'completed', 'failed', 'cancelled')),
    provider_reference TEXT NOT NULL DEFAULT '',
    failure_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0)
);

CREATE INDEX payments_user_created_id_idx
    ON payments (user_id, created_at DESC, id DESC);
CREATE INDEX payments_status_idx ON payments (status);
CREATE INDEX payments_created_at_idx ON payments (created_at DESC);

CREATE TABLE outbox_events (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    aggregate_id UUID NOT NULL,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    request_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT NOT NULL DEFAULT ''
);

CREATE INDEX outbox_events_unpublished_idx
    ON outbox_events (created_at, id)
    WHERE published_at IS NULL;
CREATE INDEX outbox_events_aggregate_idx
    ON outbox_events (aggregate_id, created_at);

CREATE TABLE consumer_inbox (
    consumer TEXT NOT NULL,
    event_id UUID NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (consumer, event_id)
);

CREATE INDEX consumer_inbox_processed_at_idx
    ON consumer_inbox (processed_at);

CREATE TABLE receipt_requests (
    payment_id UUID PRIMARY KEY REFERENCES payments (id) ON DELETE CASCADE,
    event_id UUID NOT NULL DEFAULT uuidv7() UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE receipts (
    payment_id UUID PRIMARY KEY REFERENCES payments (id) ON DELETE CASCADE,
    event_id UUID NOT NULL UNIQUE,
    object_key TEXT NOT NULL UNIQUE,
    content JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE risk_decisions (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    payment_id UUID NOT NULL REFERENCES payments (id) ON DELETE CASCADE,
    rules_version TEXT NOT NULL,
    score INTEGER NOT NULL CHECK (score BETWEEN 0 AND 100),
    decision TEXT NOT NULL CHECK (decision IN ('allow', 'deny')),
    reasons JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (payment_id, rules_version)
);

CREATE TABLE ledger_journals (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    event_id UUID NOT NULL UNIQUE,
    payment_id UUID NOT NULL UNIQUE,
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    amount BIGINT NOT NULL CHECK (amount > 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE ledger_entries (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    journal_id UUID NOT NULL REFERENCES ledger_journals (id) ON DELETE CASCADE,
    account TEXT NOT NULL CHECK (length(btrim(account)) > 0),
    direction TEXT NOT NULL CHECK (direction IN ('debit', 'credit')),
    amount BIGINT NOT NULL CHECK (amount > 0),
    currency CHAR(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX ledger_entries_journal_idx ON ledger_entries (journal_id);
CREATE INDEX ledger_entries_account_created_idx
    ON ledger_entries (account, created_at DESC);

CREATE TABLE notification_deliveries (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    event_id UUID NOT NULL UNIQUE,
    payment_id UUID NOT NULL,
    user_id UUID NOT NULL,
    payload JSONB NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'published')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);

CREATE INDEX notification_deliveries_pending_idx
    ON notification_deliveries (created_at, id)
    WHERE status = 'pending';

CREATE TABLE notification_sends (
    delivery_id UUID PRIMARY KEY REFERENCES notification_deliveries (id) ON DELETE CASCADE,
    payment_id UUID NOT NULL,
    user_id UUID NOT NULL,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMIT;
