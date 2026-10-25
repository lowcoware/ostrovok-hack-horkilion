-- Runs once, on an empty data directory. Product funnel analytics live next to
-- Langfuse in the same ClickHouse, in their own database.
CREATE DATABASE IF NOT EXISTS analytics;

CREATE TABLE IF NOT EXISTS analytics.events
(
    event_time DateTime64(3, 'UTC'),
    event      LowCardinality(String),
    session_id String,
    stage      LowCardinality(String),
    props      String
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(event_time)
ORDER BY (event, event_time);
