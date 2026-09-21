CREATE DATABASE IF NOT EXISTS payment_analytics;

CREATE TABLE IF NOT EXISTS payment_analytics.payment_events
(
    event_id UUID,
    event_type LowCardinality(String),
    occurred_at DateTime64(3, 'UTC'),
    payment_id UUID,
    user_id UUID,
    amount Int64,
    currency LowCardinality(FixedString(3)),
    description String,
    status LowCardinality(String),
    provider_reference String,
    failure_reason String,
    aggregate_version UInt64,
    created_at DateTime64(3, 'UTC'),
    updated_at DateTime64(3, 'UTC'),
    correlation_id String,
    causation_id String,
    kafka_topic LowCardinality(String),
    kafka_partition Int32,
    kafka_offset Int64,
    ingested_at DateTime64(3, 'UTC')
)
ENGINE = ReplacingMergeTree(ingested_at)
PARTITION BY toYYYYMM(occurred_at)
ORDER BY event_id
TTL occurred_at + INTERVAL 730 DAY DELETE
SETTINGS index_granularity = 8192;

-- One row per payment, reconstructed from immutable lifecycle events.
CREATE VIEW IF NOT EXISTS payment_analytics.payment_current AS
SELECT
    event.payment_id,
    argMax(event.user_id, tuple(event.aggregate_version, event.ingested_at, toString(event.event_id))) AS user_id,
    argMax(event.amount, tuple(event.aggregate_version, event.ingested_at, toString(event.event_id))) AS amount,
    argMax(event.currency, tuple(event.aggregate_version, event.ingested_at, toString(event.event_id))) AS currency,
    argMax(event.description, tuple(event.aggregate_version, event.ingested_at, toString(event.event_id))) AS description,
    argMax(event.status, tuple(event.aggregate_version, event.ingested_at, toString(event.event_id))) AS status,
    argMax(event.provider_reference, tuple(event.aggregate_version, event.ingested_at, toString(event.event_id))) AS provider_reference,
    argMax(event.failure_reason, tuple(event.aggregate_version, event.ingested_at, toString(event.event_id))) AS failure_reason,
    max(event.aggregate_version) AS aggregate_version,
    argMax(event.created_at, tuple(event.aggregate_version, event.ingested_at, toString(event.event_id))) AS created_at,
    argMax(event.updated_at, tuple(event.aggregate_version, event.ingested_at, toString(event.event_id))) AS updated_at
FROM payment_analytics.payment_events AS event FINAL
GROUP BY event.payment_id;

CREATE VIEW IF NOT EXISTS payment_analytics.payment_daily_kpis AS
SELECT
    toDate(created_at) AS day,
    currency,
    count() AS payments_created,
    countIf(status = 'completed') AS payments_completed,
    countIf(status = 'failed') AS payments_failed,
    countIf(status = 'cancelled') AS payments_cancelled,
    sumIf(amount, status = 'completed') AS completed_volume_minor,
    round(100.0 * payments_completed / nullIf(payments_completed + payments_failed, 0), 2) AS approval_rate_pct
FROM payment_analytics.payment_current
GROUP BY day, currency;
