package app

import (
	"context"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

// ReservationCommittedPayload is the InventoryReservationCommitted event body.
type ReservationCommittedPayload struct {
	ReservationID string `json:"reservation_id"`
}

// ReservationReleasedPayload is the InventoryReservationReleased event body.
type ReservationReleasedPayload struct {
	ReservationID string `json:"reservation_id"`
}

// Manage commits and releases existing reservations and adjusts stock levels.
type Manage struct {
	store InventoryStore
}

// NewManage builds the use case.
func NewManage(store InventoryStore) *Manage {
	return &Manage{store: store}
}

// Commit moves the reservation's quantity out of reserved stock permanently.
// Returns domain.ErrIllegalTransition when the reservation is not reserved.
func (uc *Manage) Commit(ctx context.Context, id string) (domain.Reservation, error) {
	event, err := outbox.NewEvent("reservation", id, "InventoryReservationCommitted",
		ReservationCommittedPayload{ReservationID: id})
	if err != nil {
		return domain.Reservation{}, err
	}
	return uc.store.Commit(ctx, id, []outbox.Event{event})
}

// Release returns the reservation's quantity to available stock. Returns
// domain.ErrIllegalTransition when the reservation is not reserved.
func (uc *Manage) Release(ctx context.Context, id string) (domain.Reservation, error) {
	event, err := outbox.NewEvent("reservation", id, "InventoryReservationReleased",
		ReservationReleasedPayload{ReservationID: id})
	if err != nil {
		return domain.Reservation{}, err
	}
	return uc.store.Release(ctx, id, []outbox.Event{event})
}

// SetStock sets the available quantity for sku.
func (uc *Manage) SetStock(ctx context.Context, sku string, quantity int) (domain.Item, error) {
	return uc.store.SetStock(ctx, sku, quantity)
}
