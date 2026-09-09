package domain

import "errors"

var (
	// ErrIllegalTransition is returned for an edge not present in the state machine.
	ErrIllegalTransition = errors.New("illegal reservation status transition")
	// ErrReservationNotFound is returned when no reservation exists for an id.
	ErrReservationNotFound = errors.New("reservation not found")
	// ErrInsufficientStock is returned when an item cannot cover a requested quantity.
	ErrInsufficientStock = errors.New("insufficient stock")
	// ErrMissingOrderID is returned when order_id is empty.
	ErrMissingOrderID = errors.New("order_id is required")
	// ErrMissingSKU is returned when sku is empty.
	ErrMissingSKU = errors.New("sku is required")
	// ErrMissingKey is returned when idempotency_key is empty.
	ErrMissingKey = errors.New("idempotency_key is required")
	// ErrInvalidQuantity is returned when quantity is not positive.
	ErrInvalidQuantity = errors.New("quantity must be positive")
)
