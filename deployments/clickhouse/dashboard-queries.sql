-- Grafana: created/completed/failed payments and approval rate by day.
SELECT
    day AS time,
    currency,
    payments_created,
    payments_completed,
    payments_failed,
    approval_rate_pct
FROM payment_analytics.payment_daily_kpis
WHERE day BETWEEN toDate($__fromTime) AND toDate($__toTime)
ORDER BY time, currency;

-- Grafana: successfully processed volume in currency minor units. Conversion
-- to major units belongs in the presentation layer because ISO-4217 exponents
-- are currency-specific (for example JPY=0, USD=2, KWD=3).
SELECT
    day AS time,
    currency,
    completed_volume_minor
FROM payment_analytics.payment_daily_kpis
WHERE day BETWEEN toDate($__fromTime) AND toDate($__toTime)
ORDER BY time, currency;

-- Operations: event ingestion delay p50/p95/p99 over five-minute buckets.
SELECT
    toStartOfFiveMinutes(occurred_at) AS time,
    quantile(0.50)(dateDiff('millisecond', occurred_at, ingested_at)) AS p50_ms,
    quantile(0.95)(dateDiff('millisecond', occurred_at, ingested_at)) AS p95_ms,
    quantile(0.99)(dateDiff('millisecond', occurred_at, ingested_at)) AS p99_ms
FROM payment_analytics.payment_events FINAL
WHERE occurred_at BETWEEN $__fromTime AND $__toTime
GROUP BY time
ORDER BY time;

-- Product/risk: terminal failures by reason.
SELECT
    failure_reason,
    count() AS payments
FROM payment_analytics.payment_current
WHERE status = 'failed'
  AND updated_at BETWEEN $__fromTime AND $__toTime
GROUP BY failure_reason
ORDER BY payments DESC
LIMIT 20;
