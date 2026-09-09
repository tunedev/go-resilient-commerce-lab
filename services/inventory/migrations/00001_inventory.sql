-- +goose Up
CREATE TABLE inventory_items (
    sku                text        PRIMARY KEY,
    available_quantity integer     NOT NULL CHECK (available_quantity >= 0),
    reserved_quantity  integer     NOT NULL DEFAULT 0 CHECK (reserved_quantity >= 0),
    version            bigint      NOT NULL DEFAULT 0,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE inventory_reservations (
    id              text        PRIMARY KEY,
    order_id        text        NOT NULL,
    sku             text        NOT NULL REFERENCES inventory_items (sku),
    quantity        integer     NOT NULL CHECK (quantity > 0),
    status          text        NOT NULL,
    expires_at      timestamptz NOT NULL,
    idempotency_key text        NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT inventory_reservations_status_valid
        CHECK (status IN ('reserved', 'released', 'committed', 'expired'))
);

CREATE INDEX inventory_reservations_sweepable
    ON inventory_reservations (expires_at)
    WHERE status = 'reserved';

-- +goose Down
DROP TABLE inventory_reservations;
DROP TABLE inventory_items;
