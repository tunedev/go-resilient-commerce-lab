package app

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

// ReservationExpiredPayload is the InventoryReservationExpired event body.
type ReservationExpiredPayload struct {
	ReservationID string `json:"reservation_id"`
}

// SweeperOptions configures the sweep loop.
type SweeperOptions struct {
	Interval  time.Duration
	BatchSize int
}

// Sweeper releases reservations whose hold has lapsed back to available
// stock.
type Sweeper struct {
	store  InventoryStore
	logger *slog.Logger
	opts   SweeperOptions
}

// NewSweeper builds a sweeper. Several may run concurrently against the same
// store.
func NewSweeper(store InventoryStore, logger *slog.Logger, opts SweeperOptions) *Sweeper {
	return &Sweeper{store: store, logger: logger, opts: opts}
}

// Run expires due reservations on every tick of opts.Interval until ctx is
// cancelled, then returns nil. A failing batch is logged, not returned, so
// the loop keeps running and retries on the next tick.
func (s *Sweeper) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.sweepBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
				s.logger.ErrorContext(ctx, "inventory sweep", slog.Any("error", err))
			}
		}
	}
}

func (s *Sweeper) sweepBatch(ctx context.Context) error {
	n, err := s.store.ExpireDue(ctx, s.opts.BatchSize, buildExpiredEvent)
	if err != nil {
		return err
	}
	if n > 0 {
		s.logger.InfoContext(ctx, "expired reservations", slog.Int("count", n))
	}
	return nil
}

func buildExpiredEvent(r domain.Reservation) (outbox.Event, error) {
	return outbox.NewEvent("reservation", r.ID, "InventoryReservationExpired",
		ReservationExpiredPayload{ReservationID: r.ID})
}
