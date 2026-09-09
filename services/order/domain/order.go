// Package domain holds the order state machine. It performs no I/O.
package domain

import (
	"fmt"
	"time"
)

// Status is the lifecycle state of an order.
type Status string

// The order lifecycle.
const (
	StatusPending                Status = "pending"
	StatusInventoryReserved      Status = "inventory_reserved"
	StatusPaymentPending         Status = "payment_pending"
	StatusPaymentSucceeded       Status = "payment_succeeded"
	StatusPaymentFailed          Status = "payment_failed"
	StatusConfirmed              Status = "confirmed"
	StatusCancelled              Status = "cancelled"
	StatusExpired                Status = "expired"
	StatusAwaitingReconciliation Status = "awaiting_reconciliation"
	StatusManualReview           Status = "manual_review"
)

// transitions is the complete set of legal edges. A status absent as a key is
// terminal. StatusAwaitingReconciliation deliberately has no edge to
// StatusCancelled: an unknown payment must never trigger compensation.
var transitions = map[Status][]Status{
	StatusPending:                {StatusInventoryReserved, StatusCancelled, StatusExpired},
	StatusInventoryReserved:      {StatusPaymentPending, StatusCancelled},
	StatusPaymentPending:         {StatusPaymentSucceeded, StatusPaymentFailed, StatusAwaitingReconciliation},
	StatusPaymentSucceeded:       {StatusConfirmed, StatusManualReview},
	StatusPaymentFailed:          {StatusCancelled},
	StatusAwaitingReconciliation: {StatusPaymentSucceeded, StatusPaymentFailed, StatusManualReview},
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

// Item is one line of an order.
type Item struct {
	SKU       string
	Quantity  int
	UnitPrice int64
}

// Order is the aggregate root.
type Order struct {
	ID              string
	CustomerID      string
	Status          Status
	TotalAmount     int64
	Currency        string
	PaymentMethodID string
	Items           []Item
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// TransitionTo moves the order to next, or returns ErrIllegalTransition and
// leaves the order unchanged.
func (o *Order) TransitionTo(next Status) error {
	if !o.Status.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, o.Status, next)
	}
	o.Status = next
	return nil
}

// Total returns the sum of the line items in minor currency units.
func Total(items []Item) int64 {
	var total int64
	for _, it := range items {
		total += it.UnitPrice * int64(it.Quantity)
	}
	return total
}

// Validate checks the fields a caller supplies when creating an order.
func Validate(customerID, paymentMethodID string, items []Item) error {
	if customerID == "" {
		return ErrMissingCustomer
	}
	if paymentMethodID == "" {
		return ErrMissingPaymentMethod
	}
	if len(items) == 0 {
		return ErrNoItems
	}
	for _, it := range items {
		if it.Quantity <= 0 {
			return fmt.Errorf("%w: sku %s", ErrInvalidQuantity, it.SKU)
		}
	}
	return nil
}
