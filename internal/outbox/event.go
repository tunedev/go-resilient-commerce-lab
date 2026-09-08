// Package outbox stores domain events in the same transaction as the state
// change that produced them, and publishes them afterwards.
package outbox

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// Schema is the outbox_events DDL. Each service copies it into its own
// migrations; the services own separate databases and separate histories.
const Schema = `
CREATE TABLE outbox_events (
    id              uuid PRIMARY KEY,
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
        CHECK (status IN ('pending','published','retrying','dead_lettered'))
);

CREATE INDEX outbox_events_claimable
    ON outbox_events (next_attempt_at)
    WHERE status IN ('pending','retrying');
`

// Status values for an outbox row.
const (
	StatusPending      = "pending"
	StatusPublished    = "published"
	StatusRetrying     = "retrying"
	StatusDeadLettered = "dead_lettered"
)

// Event is one domain event awaiting publication.
type Event struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   string
	EventType     string
	Payload       json.RawMessage
}

// NewEvent marshals payload and assigns the event id. The id is stable for the
// life of the row and becomes the deduplication key for consumers.
func NewEvent(aggregateType, aggregateID, eventType string, payload any) (Event, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("outbox: marshal %s payload: %w", eventType, err)
	}
	return Event{
		ID:            uuid.New(),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     eventType,
		Payload:       body,
	}, nil
}
