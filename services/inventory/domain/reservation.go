// Package domain holds the inventory reservation state machine. It performs no I/O.
package domain

import (
	"fmt"
	"time"
)

// Status is the lifecycle state of a reservation.
type Status string

// The reservation lifecycle.
const (
	StatusReserved  Status = "reserved"
	StatusReleased  Status = "released"
	StatusCommitted Status = "committed"
	StatusExpired   Status = "expired"
)

// transitions is the complete set of legal edges. A status absent as a key is
// terminal. StatusCommitted deliberately has no edge to StatusReleased:
// committed stock has been sold, and releasing it would return units to
// available quantity that a customer has already bought.
var transitions = map[Status][]Status{
	StatusReserved: {StatusReleased, StatusCommitted, StatusExpired},
}

// CanTransitionTo reports whether from -> next is a legal edge.
func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Reservation holds a hold of stock against an order.
type Reservation struct {
	ID             string
	OrderID        string
	SKU            string
	Quantity       int
	Status         Status
	ExpiresAt      time.Time
	IdempotencyKey string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Item is the stock record for a sku.
type Item struct {
	SKU               string
	AvailableQuantity int
	ReservedQuantity  int
	Version           int64
}

// TransitionTo moves the reservation to next, or returns ErrIllegalTransition
// and leaves the reservation unchanged.
func (r *Reservation) TransitionTo(next Status) error {
	if !r.Status.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, r.Status, next)
	}
	r.Status = next
	return nil
}

// Validate checks the fields a caller supplies when creating a reservation.
func Validate(orderID, sku string, quantity int, idempotencyKey string) error {
	if orderID == "" {
		return ErrMissingOrderID
	}
	if sku == "" {
		return ErrMissingSKU
	}
	if idempotencyKey == "" {
		return ErrMissingKey
	}
	if quantity <= 0 {
		return fmt.Errorf("%w: sku %s", ErrInvalidQuantity, sku)
	}
	return nil
}
