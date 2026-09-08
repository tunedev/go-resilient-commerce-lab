package outbox

import (
	"context"
	"log/slog"
)

// Sink delivers a published event. Epic E adds a Kafka implementation; changing
// transport is a one-line change at the composition root.
type Sink interface {
	Publish(ctx context.Context, e Event) error
}

// LogSink records the event and reports success. It is the only sink until
// Redpanda arrives in Epic E.
type LogSink struct {
	Logger *slog.Logger
}

// Publish logs the event.
func (s LogSink) Publish(ctx context.Context, e Event) error {
	s.Logger.InfoContext(ctx, "outbox event published",
		slog.String("event_id", e.ID.String()),
		slog.String("event_type", e.EventType),
		slog.String("aggregate_type", e.AggregateType),
		slog.String("aggregate_id", e.AggregateID),
	)
	return nil
}
