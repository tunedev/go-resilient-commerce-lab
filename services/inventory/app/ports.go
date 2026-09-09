// Package app holds the inventory service use cases. Port interfaces are
// declared here, on the consumer side, and implemented under adapter/.
package app

import (
	"context"
	"errors"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

// ErrDuplicateKey is returned by InventoryStore.Reserve when the
// idempotency_key unique constraint rejects the insert: a concurrent request
// carrying the same key has already won.
var ErrDuplicateKey = errors.New("idempotency key already claimed")

// InventoryStore persists reservations and inventory items.
type InventoryStore interface {
	// Reserve writes the reservation, decrements available stock and appends the
	// events in one transaction. Returns domain.ErrInsufficientStock when the
	// stock guard rejects the decrement, or ErrDuplicateKey when the
	// idempotency_key unique constraint rejects the insert.
	Reserve(ctx context.Context, r domain.Reservation, events []outbox.Event) error

	// ReservationByKey returns the reservation carrying the idempotency key, or
	// domain.ErrReservationNotFound.
	ReservationByKey(ctx context.Context, key string) (domain.Reservation, error)

	// Commit moves quantity out of reserved_quantity. Release returns it to
	// available_quantity. Both return domain.ErrIllegalTransition when the
	// reservation is not in the reserved state.
	Commit(ctx context.Context, id string, events []outbox.Event) (domain.Reservation, error)
	Release(ctx context.Context, id string, events []outbox.Event) (domain.Reservation, error)

	SetStock(ctx context.Context, sku string, quantity int) (domain.Item, error)

	// AppendEvents writes events in their own transaction. Used only for
	// InventoryReservationFailed: insufficient stock changes no state, so the
	// event has nothing to be atomic with, and Reserve's transaction has
	// already rolled back by the time the caller knows.
	AppendEvents(ctx context.Context, events []outbox.Event) error

	// There is deliberately no GetReservation. No endpoint reads a reservation
	// by id, and Commit and Release resolve their own ambiguity internally.

	// ExpireDue expires reservations past expires_at, up to limit, in one
	// transaction. buildEvent is called per claimed reservation so event shape
	// stays in app rather than the adapter.
	ExpireDue(ctx context.Context, limit int, buildEvent func(domain.Reservation) (outbox.Event, error)) (int, error)
}
