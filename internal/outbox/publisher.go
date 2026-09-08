package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

// PublisherOptions configures the publish loop.
type PublisherOptions struct {
	Interval    time.Duration
	BatchSize   int
	MaxRetries  int
	BaseBackoff time.Duration
}

// Publisher drains outbox_events into a Sink.
type Publisher struct {
	pool   *pgxpool.Pool
	sink   Sink
	logger *slog.Logger
	opts   PublisherOptions
}

// NewPublisher builds a publisher. Several may run concurrently against the
// same table.
func NewPublisher(pool *pgxpool.Pool, sink Sink, logger *slog.Logger, opts PublisherOptions) *Publisher {
	return &Publisher{pool: pool, sink: sink, logger: logger, opts: opts}
}

// Run drains the outbox until ctx is cancelled, then returns nil.
func (p *Publisher) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.opts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.drainBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.ErrorContext(ctx, "outbox drain", slog.Any("error", err))
			}
		}
	}
}

type claimed struct {
	Event      Event
	RetryCount int
}

// drainBatch claims one batch and publishes it. The whole batch is handled
// inside a single transaction so the row locks are held for the publish
// attempt, which is what stops a second publisher taking the same rows.
func (p *Publisher) drainBatch(ctx context.Context) error {
	return postgres.WithTx(ctx, p.pool, func(tx pgx.Tx) error {
		batch, err := claim(ctx, tx, p.opts.BatchSize)
		if err != nil {
			return err
		}
		for _, c := range batch {
			if err := p.sink.Publish(ctx, c.Event); err != nil {
				if err := p.recordFailure(ctx, tx, c, err); err != nil {
					return err
				}
				continue
			}
			if err := markPublished(ctx, tx, c.Event.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func claim(ctx context.Context, tx pgx.Tx, batchSize int) ([]claimed, error) {
	const query = `
SELECT id, aggregate_type, aggregate_id, event_type, payload, retry_count
  FROM outbox_events
 WHERE status IN ('pending', 'retrying')
   AND next_attempt_at <= now()
 ORDER BY created_at
 FOR UPDATE SKIP LOCKED
 LIMIT $1`

	rows, err := tx.Query(ctx, query, batchSize)
	if err != nil {
		return nil, fmt.Errorf("outbox: claim: %w", err)
	}
	defer rows.Close()

	var batch []claimed
	for rows.Next() {
		var c claimed
		if err := rows.Scan(&c.Event.ID, &c.Event.AggregateType, &c.Event.AggregateID,
			&c.Event.EventType, &c.Event.Payload, &c.RetryCount); err != nil {
			return nil, fmt.Errorf("outbox: scan claimed: %w", err)
		}
		batch = append(batch, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox: claim rows: %w", err)
	}
	return batch, nil
}

func markPublished(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	const query = `
UPDATE outbox_events
   SET status = 'published', published_at = now(), last_error = NULL
 WHERE id = $1`

	if _, err := tx.Exec(ctx, query, id); err != nil {
		return fmt.Errorf("outbox: mark published: %w", err)
	}
	return nil
}

func (p *Publisher) recordFailure(ctx context.Context, tx pgx.Tx, c claimed, cause error) error {
	attempt := c.RetryCount + 1

	status := StatusRetrying
	if attempt >= p.opts.MaxRetries {
		status = StatusDeadLettered
		p.logger.ErrorContext(ctx, "outbox event dead lettered",
			slog.String("event_id", c.Event.ID.String()),
			slog.String("event_type", c.Event.EventType),
			slog.Int("attempts", attempt),
			slog.Any("error", cause),
		)
	}

	const query = `
UPDATE outbox_events
   SET status = $2, retry_count = $3, last_error = $4, next_attempt_at = now() + $5::interval
 WHERE id = $1`

	backoff := p.opts.BaseBackoff << min(attempt-1, 16)
	_, err := tx.Exec(ctx, query, c.Event.ID, status, attempt, cause.Error(), backoff.String())
	if err != nil {
		return fmt.Errorf("outbox: record failure: %w", err)
	}
	return nil
}
