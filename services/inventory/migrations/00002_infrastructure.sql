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

-- +goose Down
DROP INDEX outbox_events_claimable;
DROP TABLE outbox_events;
