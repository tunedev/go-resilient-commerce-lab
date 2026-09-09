package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

type fakeStore struct {
	order      domain.Order
	events     []outbox.Event
	completion *idempotency.Completion
	err        error
	calls      int
}

func (f *fakeStore) CreateOrder(_ context.Context, o domain.Order, e []outbox.Event, c *idempotency.Completion) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.order, f.events, f.completion = o, e, c
	return nil
}

func (f *fakeStore) GetOrder(_ context.Context, id string) (domain.Order, error) {
	if f.order.ID != id {
		return domain.Order{}, domain.ErrOrderNotFound
	}
	return f.order, nil
}

type fakePricer struct{ err error }

func (f fakePricer) UnitPrice(_ context.Context, sku string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if sku == "playstation-5" {
		return 150000, nil
	}
	return 50000, nil
}

func (f fakePricer) Currency() string { return "NGN" }

func newUseCase(store app.OrderStore) *app.CreateOrder {
	return app.NewCreateOrder(store, fakePricer{}, func() string { return "ord_test" })
}

func validCommand() app.CreateOrderCommand {
	return app.CreateOrderCommand{
		CustomerID:      "cust_1",
		PaymentMethodID: "pm_ok",
		Items:           []domain.Item{{SKU: "playstation-5", Quantity: 2}},
	}
}

func TestCreateOrderPersistsPendingOrderWithPricedItems(t *testing.T) {
	store := &fakeStore{}

	result, err := newUseCase(store).Execute(context.Background(), validCommand(), "key-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.OrderID != "ord_test" || result.Status != string(domain.StatusPending) {
		t.Fatalf("result = %+v", result)
	}
	if store.order.TotalAmount != 300000 {
		t.Errorf("TotalAmount = %d, want 300000", store.order.TotalAmount)
	}
	if store.order.Currency != "NGN" {
		t.Errorf("Currency = %q, want NGN", store.order.Currency)
	}
	if store.order.Items[0].UnitPrice != 150000 {
		t.Errorf("UnitPrice = %d, want 150000", store.order.Items[0].UnitPrice)
	}
}

func TestCreateOrderEmitsOrderCreated(t *testing.T) {
	store := &fakeStore{}

	if _, err := newUseCase(store).Execute(context.Background(), validCommand(), "key-1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(store.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(store.events))
	}
	e := store.events[0]
	if e.EventType != "OrderCreated" {
		t.Errorf("EventType = %q", e.EventType)
	}
	if e.AggregateType != "order" || e.AggregateID != "ord_test" {
		t.Errorf("aggregate = %s/%s", e.AggregateType, e.AggregateID)
	}
	if e.ID == uuid.Nil {
		t.Error("event has no id")
	}
}

func TestCreateOrderPassesIdempotencyCompletion(t *testing.T) {
	store := &fakeStore{}

	result, err := newUseCase(store).Execute(context.Background(), validCommand(), "key-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if store.completion == nil {
		t.Fatal("no completion passed to the store")
	}
	if store.completion.Key != "key-1" || store.completion.ResourceID != "ord_test" {
		t.Errorf("completion = %+v", store.completion)
	}
	if store.completion.StatusCode != http.StatusAccepted {
		t.Errorf("StatusCode = %d, want 202", store.completion.StatusCode)
	}

	var replayed app.CreateOrderResult
	if err := json.Unmarshal(store.completion.ResponseBody, &replayed); err != nil {
		t.Fatalf("stored body is not the result: %v", err)
	}
	if replayed != result {
		t.Errorf("stored body %+v does not match the returned result %+v", replayed, result)
	}
}

func TestCreateOrderWithoutKeyPassesNoCompletion(t *testing.T) {
	store := &fakeStore{}

	if _, err := newUseCase(store).Execute(context.Background(), validCommand(), ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if store.completion != nil {
		t.Errorf("completion = %+v, want nil", store.completion)
	}
}

func TestCreateOrderRejectsInvalidCommandBeforeTouchingTheStore(t *testing.T) {
	cases := []struct {
		name string
		cmd  app.CreateOrderCommand
		want error
	}{
		{"no customer", app.CreateOrderCommand{PaymentMethodID: "pm_ok",
			Items: []domain.Item{{SKU: "playstation-5", Quantity: 1}}}, domain.ErrMissingCustomer},
		{"no payment method", app.CreateOrderCommand{CustomerID: "cust_1",
			Items: []domain.Item{{SKU: "playstation-5", Quantity: 1}}}, domain.ErrMissingPaymentMethod},
		{"no items", app.CreateOrderCommand{CustomerID: "cust_1",
			PaymentMethodID: "pm_ok"}, domain.ErrNoItems},
		{"zero quantity", app.CreateOrderCommand{CustomerID: "cust_1", PaymentMethodID: "pm_ok",
			Items: []domain.Item{{SKU: "playstation-5", Quantity: 0}}}, domain.ErrInvalidQuantity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			_, err := newUseCase(store).Execute(context.Background(), tc.cmd, "key-1")
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if store.calls != 0 {
				t.Error("the store was called for an invalid command")
			}
		})
	}
}

func TestCreateOrderSurfacesUnknownSKU(t *testing.T) {
	store := &fakeStore{}
	uc := app.NewCreateOrder(store, fakePricer{err: app.ErrUnknownSKU}, func() string { return "ord_test" })

	_, err := uc.Execute(context.Background(), validCommand(), "key-1")
	if !errors.Is(err, app.ErrUnknownSKU) {
		t.Fatalf("err = %v, want ErrUnknownSKU", err)
	}
	if store.calls != 0 {
		t.Error("the store was called after a pricing failure")
	}
}
