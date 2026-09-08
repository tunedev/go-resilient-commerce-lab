-- +goose Up
CREATE TABLE orders (
    id                text        PRIMARY KEY,
    customer_id       text        NOT NULL,
    status            text        NOT NULL,
    total_amount      bigint      NOT NULL CHECK (total_amount >= 0),
    currency          text        NOT NULL,
    payment_method_id text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT orders_status_valid CHECK (status IN (
        'pending', 'inventory_reserved', 'payment_pending', 'payment_succeeded',
        'payment_failed', 'confirmed', 'cancelled', 'expired',
        'awaiting_reconciliation', 'manual_review'
    ))
);

CREATE TABLE order_items (
    order_id   text    NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    sku        text    NOT NULL,
    quantity   integer NOT NULL CHECK (quantity > 0),
    unit_price bigint  NOT NULL CHECK (unit_price >= 0),
    PRIMARY KEY (order_id, sku)
);

-- +goose Down
DROP TABLE order_items;
DROP TABLE orders;
