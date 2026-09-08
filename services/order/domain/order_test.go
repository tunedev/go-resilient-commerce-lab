package domain_test

import (
	"errors"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

func TestLegalTransitions(t *testing.T) {
	legal := []struct{ from, to domain.Status }{
		{domain.StatusPending, domain.StatusInventoryReserved},
		{domain.StatusPending, domain.StatusCancelled},
		{domain.StatusPending, domain.StatusExpired},
		{domain.StatusInventoryReserved, domain.StatusPaymentPending},
		{domain.StatusInventoryReserved, domain.StatusCancelled},
		{domain.StatusPaymentPending, domain.StatusPaymentSucceeded},
		{domain.StatusPaymentPending, domain.StatusPaymentFailed},
		{domain.StatusPaymentPending, domain.StatusAwaitingReconciliation},
		{domain.StatusPaymentSucceeded, domain.StatusConfirmed},
		{domain.StatusPaymentSucceeded, domain.StatusManualReview},
		{domain.StatusPaymentFailed, domain.StatusCancelled},
		{domain.StatusAwaitingReconciliation, domain.StatusPaymentSucceeded},
		{domain.StatusAwaitingReconciliation, domain.StatusPaymentFailed},
		{domain.StatusAwaitingReconciliation, domain.StatusManualReview},
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
		{domain.StatusPending, domain.StatusConfirmed},
		{domain.StatusPending, domain.StatusPaymentSucceeded},
		{domain.StatusConfirmed, domain.StatusCancelled},
		{domain.StatusCancelled, domain.StatusPending},
		{domain.StatusExpired, domain.StatusConfirmed},
		{domain.StatusManualReview, domain.StatusConfirmed},
		{domain.StatusAwaitingReconciliation, domain.StatusCancelled},
	}

	for _, tc := range illegal {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			if tc.from.CanTransitionTo(tc.to) {
				t.Errorf("%s -> %s allowed, want rejected", tc.from, tc.to)
			}
		})
	}
}

func TestAwaitingReconciliationNeverReachesCancelled(t *testing.T) {
	if domain.StatusAwaitingReconciliation.CanTransitionTo(domain.StatusCancelled) {
		t.Fatal("an unknown payment must never be cancellable; releasing inventory " +
			"while a charge may have landed is the bug this lab exists to demonstrate")
	}
}

func TestTransitionToUpdatesStatus(t *testing.T) {
	o := &domain.Order{Status: domain.StatusPending}

	if err := o.TransitionTo(domain.StatusInventoryReserved); err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}
	if o.Status != domain.StatusInventoryReserved {
		t.Errorf("Status = %q, want %q", o.Status, domain.StatusInventoryReserved)
	}
}

func TestTransitionToRejectsIllegalEdge(t *testing.T) {
	o := &domain.Order{Status: domain.StatusPending}

	err := o.TransitionTo(domain.StatusConfirmed)
	if !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
	if o.Status != domain.StatusPending {
		t.Errorf("Status changed to %q on a rejected transition", o.Status)
	}
}

func TestValidate(t *testing.T) {
	good := []domain.Item{{SKU: "playstation-5", Quantity: 1, UnitPrice: 150000}}

	cases := []struct {
		name       string
		customerID string
		method     string
		items      []domain.Item
		want       error
	}{
		{"valid", "cust_1", "pm_ok", good, nil},
		{"no customer", "", "pm_ok", good, domain.ErrMissingCustomer},
		{"no payment method", "cust_1", "", good, domain.ErrMissingPaymentMethod},
		{"no items", "cust_1", "pm_ok", nil, domain.ErrNoItems},
		{"zero quantity", "cust_1", "pm_ok",
			[]domain.Item{{SKU: "playstation-5", Quantity: 0}}, domain.ErrInvalidQuantity},
		{"negative quantity", "cust_1", "pm_ok",
			[]domain.Item{{SKU: "playstation-5", Quantity: -1}}, domain.ErrInvalidQuantity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.Validate(tc.customerID, tc.method, tc.items)
			if !errors.Is(err, tc.want) {
				t.Errorf("Validate = %v, want %v", err, tc.want)
			}
		})
	}
}
