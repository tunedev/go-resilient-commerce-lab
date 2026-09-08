// Package app holds the order service use cases. Port interfaces are declared
// here, on the consumer side, and implemented under adapter/.
package app

import (
	"context"
	"errors"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

// ErrUnknownSKU is returned when no price exists for a requested sku.
var ErrUnknownSKU = errors.New("unknown sku")

// OrderStore persists orders.
type OrderStore interface {
	// CreateOrder writes the order, its items, the outbox events and, when
	// completion is not nil, the idempotency completion, in one transaction.
	// Either all of them exist afterwards or none of them do.
	CreateOrder(ctx context.Context, order domain.Order, events []outbox.Event, completion *idempotency.Completion) error

	// GetOrder returns the order, or domain.ErrOrderNotFound.
	GetOrder(ctx context.Context, id string) (domain.Order, error)
}

// Pricer supplies unit prices.
type Pricer interface {
	UnitPrice(ctx context.Context, sku string) (int64, error)
	Currency() string
}
