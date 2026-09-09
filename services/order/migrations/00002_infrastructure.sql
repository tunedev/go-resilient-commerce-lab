-- +goose Up
CREATE TABLE outbox_events (
    id              uuid        PRIMARY KEY,
    aggregate_type  text        NOT NULL,
    aggregate_id    text        NOT NULL,
    event_type      text        NOT NULL,
    payload         jsonb       NOT NULL,
    status          text        NOT NULL DEFAULT 'pending',
    retry_count     integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    published_at    timestamptz,
    last_error      text,
    CONSTRAINT outbox_events_status_valid
        CHECK (status IN ('pending', 'published', 'retrying', 'dead_lettered'))
);

CREATE INDEX outbox_events_claimable
    ON outbox_events (next_attempt_at)
    WHERE status IN ('pending', 'retrying');

-- response_body is json rather than jsonb: jsonb canonicalises on storage,
-- reordering object keys, and a replay must return the original bytes.
CREATE TABLE idempotency_keys (
    key           text        PRIMARY KEY,
    request_hash  text        NOT NULL,
    state         text        NOT NULL,
    resource_type text        NOT NULL DEFAULT '',
    resource_id   text        NOT NULL DEFAULT '',
    response_body json,
    status_code   integer     NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT idempotency_keys_state_valid
        CHECK (state IN ('in_progress', 'completed'))
);

-- +goose Down
DROP TABLE idempotency_keys;
DROP INDEX outbox_events_claimable;
DROP TABLE outbox_events;
