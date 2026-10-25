-- +goose Up
CREATE SCHEMA IF NOT EXISTS agent;

-- Every agent turn writes a new version of the plan, so changes can be rolled
-- back and shown to the user as a diff.
CREATE TABLE agent.trip_plans (
    session_id uuid        NOT NULL,
    version    integer     NOT NULL,
    plan       jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, version)
);

CREATE TABLE agent.messages (
    id         bigserial PRIMARY KEY,
    session_id uuid        NOT NULL,
    role       text        NOT NULL,
    content    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX messages_session_idx ON agent.messages (session_id, id);

-- Deduplicates redelivered turn messages.
CREATE TABLE agent.processed_messages (
    message_id   text PRIMARY KEY,
    processed_at timestamptz NOT NULL DEFAULT now()
);

CREATE SCHEMA IF NOT EXISTS booking;

CREATE TABLE booking.orders (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key text        NOT NULL UNIQUE,
    session_id      uuid        NOT NULL,
    hotel_id        text        NOT NULL,
    hotel_name      text        NOT NULL,
    check_in        date        NOT NULL,
    nights          integer     NOT NULL CHECK (nights > 0),
    adults          integer     NOT NULL CHECK (adults > 0),
    children        integer     NOT NULL DEFAULT 0 CHECK (children >= 0),
    guest_name      text        NOT NULL,
    total_price_rub bigint      NOT NULL CHECK (total_price_rub > 0),
    status          text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- Transactional outbox: written in the same transaction as the order and
-- published to JetStream by the relay.
CREATE TABLE booking.outbox (
    id           bigserial PRIMARY KEY,
    subject      text        NOT NULL,
    payload      jsonb       NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    published_at timestamptz
);
CREATE INDEX outbox_unpublished_idx ON booking.outbox (id) WHERE published_at IS NULL;

-- +goose Down
DROP SCHEMA booking CASCADE;
DROP SCHEMA agent CASCADE;
