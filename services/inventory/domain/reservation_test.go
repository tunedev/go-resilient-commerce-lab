package domain_test

import (
	"errors"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

func TestLegalTransitions(t *testing.T) {
	legal := []struct{ from, to domain.Status }{
		{domain.StatusReserved, domain.StatusReleased},
		{domain.StatusReserved, domain.StatusCommitted},
		{domain.StatusReserved, domain.StatusExpired},
	}

	for _, tc := range legal {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			if !tc.from.CanTransitionTo(tc.to) {
				t.Errorf("%s -> %s rejected, want allowed", tc.from, tc.to)
			}
		})
	}
}

func TestIllegalTransitions(t *testing.T) {
	illegal := []struct{ from, to domain.Status }{
		{domain.StatusCommitted, domain.StatusReleased},
		{domain.StatusCommitted, domain.StatusExpired},
		{domain.StatusReleased, domain.StatusReserved},
		{domain.StatusReleased, domain.StatusCommitted},
		{domain.StatusExpired, domain.StatusReserved},
		{domain.StatusExpired, domain.StatusReleased},
		{domain.StatusReserved, domain.StatusReserved},
	}

	for _, tc := range illegal {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			if tc.from.CanTransitionTo(tc.to) {
				t.Errorf("%s -> %s allowed, want rejected", tc.from, tc.to)
			}
		})
	}
}

func TestCommittedReservationCanNeverBeReleased(t *testing.T) {
	if domain.StatusCommitted.CanTransitionTo(domain.StatusReleased) {
		t.Fatal("committed stock has been sold; releasing it would return units " +
			"to available quantity that a customer has already bought")
	}
}

func TestTransitionToUpdatesStatus(t *testing.T) {
	r := &domain.Reservation{Status: domain.StatusReserved}

	if err := r.TransitionTo(domain.StatusCommitted); err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}
	if r.Status != domain.StatusCommitted {
		t.Errorf("Status = %q, want %q", r.Status, domain.StatusCommitted)
	}
}

func TestTransitionToRejectsIllegalEdge(t *testing.T) {
	r := &domain.Reservation{Status: domain.StatusCommitted}

	err := r.TransitionTo(domain.StatusReleased)
	if !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
	if r.Status != domain.StatusCommitted {
		t.Errorf("Status changed to %q on a rejected transition", r.Status)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name           string
		orderID        string
		sku            string
		quantity       int
		idempotencyKey string
		want           error
	}{
		{"valid", "order_1", "playstation-5", 1, "key_1", nil},
		{"no order id", "", "playstation-5", 1, "key_1", domain.ErrMissingOrderID},
		{"no sku", "order_1", "", 1, "key_1", domain.ErrMissingSKU},
		{"no idempotency key", "order_1", "playstation-5", 1, "", domain.ErrMissingKey},
		{"zero quantity", "order_1", "playstation-5", 0, "key_1", domain.ErrInvalidQuantity},
		{"negative quantity", "order_1", "playstation-5", -1, "key_1", domain.ErrInvalidQuantity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.Validate(tc.orderID, tc.sku, tc.quantity, tc.idempotencyKey)
			if !errors.Is(err, tc.want) {
				t.Errorf("Validate = %v, want %v", err, tc.want)
			}
		})
	}
}
