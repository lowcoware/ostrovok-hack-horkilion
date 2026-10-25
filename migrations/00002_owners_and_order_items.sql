-- +goose Up
-- Phase 0 contracts: orders of several items with an owner, and session
-- ownership in the gateway. Orders created before keep their id and total but
-- lose the hotel columns and have no owner, so nobody sees them in /orders.
ALTER TABLE booking.orders
    DROP COLUMN hotel_id,
    DROP COLUMN hotel_name,
    DROP COLUMN check_in,
    DROP COLUMN nights,
    DROP COLUMN adults,
    DROP COLUMN children,
    ADD COLUMN owner_id text  NOT NULL DEFAULT '',
    -- OrderItem messages as protojson, in request order.
    ADD COLUMN items    jsonb NOT NULL DEFAULT '[]';
ALTER TABLE booking.orders RENAME COLUMN total_price_rub TO total_rub;
CREATE INDEX orders_owner_idx ON booking.orders (owner_id, created_at DESC);

CREATE SCHEMA IF NOT EXISTS gateway;

-- Which owner a session belongs to. Plans and messages stay in the agent schema.
CREATE TABLE gateway.sessions (
    id         uuid PRIMARY KEY,
    owner_id   text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_owner_idx ON gateway.sessions (owner_id, created_at DESC);

-- +goose Down
DROP SCHEMA gateway CASCADE;
DROP INDEX booking.orders_owner_idx;
ALTER TABLE booking.orders RENAME COLUMN total_rub TO total_price_rub;
ALTER TABLE booking.orders
    DROP COLUMN items,
    DROP COLUMN owner_id,
    ADD COLUMN hotel_id   text    NOT NULL DEFAULT '',
    ADD COLUMN hotel_name text    NOT NULL DEFAULT '',
    ADD COLUMN check_in   date    NOT NULL DEFAULT CURRENT_DATE,
    ADD COLUMN nights     integer NOT NULL DEFAULT 1 CHECK (nights > 0),
    ADD COLUMN adults     integer NOT NULL DEFAULT 1 CHECK (adults > 0),
    ADD COLUMN children   integer NOT NULL DEFAULT 0 CHECK (children >= 0);
