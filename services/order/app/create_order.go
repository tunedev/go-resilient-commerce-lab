package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

// CreateOrderCommand is a request to create an order. Item unit prices are
// filled in by the Pricer; callers supply sku and quantity only.
type CreateOrderCommand struct {
	CustomerID      string
	PaymentMethodID string
	Items           []domain.Item
}

// CreateOrderResult is the response body for a created order. It is also what
// is stored against the idempotency key and replayed on a duplicate request.
type CreateOrderResult struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
}

// OrderCreatedPayload is the OrderCreated event body.
type OrderCreatedPayload struct {
	OrderID         string        `json:"order_id"`
	CustomerID      string        `json:"customer_id"`
	PaymentMethodID string        `json:"payment_method_id"`
	TotalAmount     int64         `json:"total_amount"`
	Currency        string        `json:"currency"`
	Items           []domain.Item `json:"items"`
}

// CreateOrder creates an order in the pending state.
type CreateOrder struct {
	store  OrderStore
	pricer Pricer
	newID  func() string
}

// NewCreateOrder builds the use case.
func NewCreateOrder(store OrderStore, pricer Pricer, newID func() string) *CreateOrder {
	return &CreateOrder{store: store, pricer: pricer, newID: newID}
}

// Execute validates, prices and persists the order together with its
// OrderCreated event and, when idempotencyKey is not empty, the completion of
// that key. The saga advances the order asynchronously from Epic D onwards.
func (uc *CreateOrder) Execute(ctx context.Context, cmd CreateOrderCommand, idempotencyKey string) (CreateOrderResult, error) {
	if err := domain.Validate(cmd.CustomerID, cmd.PaymentMethodID, cmd.Items); err != nil {
		return CreateOrderResult{}, err
	}

	items := make([]domain.Item, 0, len(cmd.Items))
	for _, requested := range cmd.Items {
		price, err := uc.pricer.UnitPrice(ctx, requested.SKU)
		if err != nil {
			return CreateOrderResult{}, err
		}
		items = append(items, domain.Item{
			SKU:       requested.SKU,
			Quantity:  requested.Quantity,
			UnitPrice: price,
		})
	}

	order := domain.Order{
		ID:              uc.newID(),
		CustomerID:      cmd.CustomerID,
		Status:          domain.StatusPending,
		TotalAmount:     domain.Total(items),
		Currency:        uc.pricer.Currency(),
		PaymentMethodID: cmd.PaymentMethodID,
		Items:           items,
	}

	result := CreateOrderResult{OrderID: order.ID, Status: string(order.Status)}

	event, err := outbox.NewEvent("order", order.ID, "OrderCreated", OrderCreatedPayload{
		OrderID:         order.ID,
		CustomerID:      order.CustomerID,
		PaymentMethodID: order.PaymentMethodID,
		TotalAmount:     order.TotalAmount,
		Currency:        order.Currency,
		Items:           order.Items,
	})
	if err != nil {
		return CreateOrderResult{}, err
	}

	var completion *idempotency.Completion
	if idempotencyKey != "" {
		body, err := json.Marshal(result)
		if err != nil {
			return CreateOrderResult{}, fmt.Errorf("app: marshal create order result: %w", err)
		}
		completion = &idempotency.Completion{
			Key:          idempotencyKey,
			ResourceType: "order",
			ResourceID:   order.ID,
			StatusCode:   http.StatusAccepted,
			ResponseBody: body,
		}
	}

	if err := uc.store.CreateOrder(ctx, order, []outbox.Event{event}, completion); err != nil {
		return CreateOrderResult{}, err
	}
	return result, nil
}
