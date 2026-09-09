package app

import (
	"context"
	"errors"
	"time"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

// ReserveCommand is a request to reserve stock against an order.
type ReserveCommand struct {
	OrderID        string
	SKU            string
	Quantity       int
	IdempotencyKey string
}

// ReservedPayload is the InventoryReserved event body.
type ReservedPayload struct {
	ReservationID string `json:"reservation_id"`
	OrderID       string `json:"order_id"`
	SKU           string `json:"sku"`
	Quantity      int    `json:"quantity"`
}

// ReservationFailedPayload is the InventoryReservationFailed event body.
type ReservationFailedPayload struct {
	ReservationID string `json:"reservation_id"`
	OrderID       string `json:"order_id"`
	SKU           string `json:"sku"`
	Quantity      int    `json:"quantity"`
	Reason        string `json:"reason"`
}

// Reserve holds stock against an order.
type Reserve struct {
	store InventoryStore
	ttl   time.Duration
	newID func() string
}

// NewReserve builds the use case. ttl is how far past now a new reservation's
// ExpiresAt is set.
func NewReserve(store InventoryStore, ttl time.Duration, newID func() string) *Reserve {
	return &Reserve{store: store, ttl: ttl, newID: newID}
}

// Execute reserves cmd.Quantity units of cmd.SKU for cmd.OrderID. A repeat
// carrying a previously used cmd.IdempotencyKey reserves nothing and returns
// the reservation that key already produced. Insufficient stock returns
// domain.ErrInsufficientStock after recording an InventoryReservationFailed
// event.
//
// The returned reservation carries a zero CreatedAt when this call is the one
// that created it, and a real CreatedAt when it was read back from storage
// (the previously-used-key and duplicate-key-race paths both read back). The
// HTTP adapter uses that distinction to answer 201 for a new reservation and
// 200 for a replay.
func (uc *Reserve) Execute(ctx context.Context, cmd ReserveCommand) (domain.Reservation, error) {
	if err := domain.Validate(cmd.OrderID, cmd.SKU, cmd.Quantity, cmd.IdempotencyKey); err != nil {
		return domain.Reservation{}, err
	}

	existing, err := uc.store.ReservationByKey(ctx, cmd.IdempotencyKey)
	switch {
	case err == nil:
		return existing, nil
	case !errors.Is(err, domain.ErrReservationNotFound):
		return domain.Reservation{}, err
	}

	reservation := domain.Reservation{
		ID:             uc.newID(),
		OrderID:        cmd.OrderID,
		SKU:            cmd.SKU,
		Quantity:       cmd.Quantity,
		Status:         domain.StatusReserved,
		ExpiresAt:      time.Now().Add(uc.ttl),
		IdempotencyKey: cmd.IdempotencyKey,
	}

	event, err := outbox.NewEvent("reservation", reservation.ID, "InventoryReserved", ReservedPayload{
		ReservationID: reservation.ID,
		OrderID:       reservation.OrderID,
		SKU:           reservation.SKU,
		Quantity:      reservation.Quantity,
	})
	if err != nil {
		return domain.Reservation{}, err
	}

	switch err := uc.store.Reserve(ctx, reservation, []outbox.Event{event}); {
	case err == nil:
		return reservation, nil
	case errors.Is(err, ErrDuplicateKey):
		return uc.store.ReservationByKey(ctx, cmd.IdempotencyKey)
	case errors.Is(err, domain.ErrInsufficientStock):
		// The stock guard is evaluated before the idempotency_key unique
		// constraint, so a concurrent retry sharing this key can lose the
		// stock race even though the key already won. Re-check before
		// declaring failure: if the key is now claimed, this call is a
		// replay of that win, not a new failure.
		winner, byKeyErr := uc.store.ReservationByKey(ctx, cmd.IdempotencyKey)
		switch {
		case byKeyErr == nil:
			return winner, nil
		case !errors.Is(byKeyErr, domain.ErrReservationNotFound):
			return domain.Reservation{}, byKeyErr
		}
		return domain.Reservation{}, uc.recordFailure(ctx, reservation)
	default:
		return domain.Reservation{}, err
	}
}

// recordFailure appends an InventoryReservationFailed event in its own
// transaction and returns domain.ErrInsufficientStock. Reserve's transaction
// has already rolled back by this point, so nothing written inside it
// survives, and insufficient stock changes no state for the event to be
// atomic with.
func (uc *Reserve) recordFailure(ctx context.Context, r domain.Reservation) error {
	event, err := outbox.NewEvent("reservation", r.ID, "InventoryReservationFailed", ReservationFailedPayload{
		ReservationID: r.ID,
		OrderID:       r.OrderID,
		SKU:           r.SKU,
		Quantity:      r.Quantity,
		Reason:        domain.ErrInsufficientStock.Error(),
	})
	if err != nil {
		return err
	}
	if err := uc.store.AppendEvents(ctx, []outbox.Event{event}); err != nil {
		return err
	}
	return domain.ErrInsufficientStock
}
