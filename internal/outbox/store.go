package outbox

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Append writes events inside the caller's transaction, so they commit with the
// state change that produced them or not at all.
func Append(ctx context.Context, tx pgx.Tx, events ...Event) error {
	const query = `
INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, payload)
VALUES ($1, $2, $3, $4, $5)`

	for _, e := range events {
		_, err := tx.Exec(ctx, query, e.ID, e.AggregateType, e.AggregateID, e.EventType, e.Payload)
		if err != nil {
			return fmt.Errorf("outbox: append %s: %w", e.EventType, err)
		}
	}
	return nil
}
