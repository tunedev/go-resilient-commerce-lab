package domain

import "errors"

var (
	// ErrIllegalTransition is returned for an edge not present in the state machine.
	ErrIllegalTransition = errors.New("illegal order status transition")
	// ErrMissingCustomer is returned when customer_id is empty.
	ErrMissingCustomer = errors.New("customer_id is required")
	// ErrMissingPaymentMethod is returned when payment_method_id is empty.
	ErrMissingPaymentMethod = errors.New("payment_method_id is required")
	// ErrNoItems is returned when an order has no line items.
	ErrNoItems = errors.New("at least one item is required")
	// ErrInvalidQuantity is returned when a line item quantity is not positive.
	ErrInvalidQuantity = errors.New("item quantity must be positive")
	// ErrOrderNotFound is returned when no order exists for an id.
	ErrOrderNotFound = errors.New("order not found")
)
