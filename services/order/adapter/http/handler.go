// Package http adapts the order use cases to HTTP.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

// Handler serves the order endpoints.
type Handler struct {
	create *app.CreateOrder
	get    *app.GetOrder
}

// NewHandler builds the handler.
func NewHandler(create *app.CreateOrder, get *app.GetOrder) *Handler {
	return &Handler{create: create, get: get}
}

type createRequest struct {
	CustomerID      string `json:"customer_id"`
	PaymentMethodID string `json:"payment_method_id"`
	Items           []struct {
		SKU      string `json:"sku"`
		Quantity int    `json:"quantity"`
	} `json:"items"`
}

type itemResponse struct {
	SKU       string `json:"sku"`
	Quantity  int    `json:"quantity"`
	UnitPrice int64  `json:"unit_price"`
}

type orderResponse struct {
	OrderID         string         `json:"order_id"`
	CustomerID      string         `json:"customer_id"`
	Status          string         `json:"status"`
	TotalAmount     int64          `json:"total_amount"`
	Currency        string         `json:"currency"`
	PaymentMethodID string         `json:"payment_method_id"`
	Items           []itemResponse `json:"items"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// Create handles POST /orders.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req createRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		httpx.WriteError(ctx, w, http.StatusBadRequest,
			"invalid_request", "the request body is not valid JSON", nil)
		return
	}

	items := make([]domain.Item, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, domain.Item{SKU: it.SKU, Quantity: it.Quantity})
	}

	key, _ := idempotency.KeyFromContext(ctx)

	result, err := h.create.Execute(ctx, app.CreateOrderCommand{
		CustomerID:      req.CustomerID,
		PaymentMethodID: req.PaymentMethodID,
		Items:           items,
	}, key)
	if err != nil {
		writeDomainError(ctx, w, err)
		return
	}

	httpx.WriteJSON(ctx, w, http.StatusAccepted, result)
}

// Get handles GET /orders/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	order, err := h.get.Execute(ctx, r.PathValue("id"))
	if err != nil {
		writeDomainError(ctx, w, err)
		return
	}

	items := make([]itemResponse, 0, len(order.Items))
	for _, it := range order.Items {
		items = append(items, itemResponse{SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice})
	}

	httpx.WriteJSON(ctx, w, http.StatusOK, orderResponse{
		OrderID:         order.ID,
		CustomerID:      order.CustomerID,
		Status:          string(order.Status),
		TotalAmount:     order.TotalAmount,
		Currency:        order.Currency,
		PaymentMethodID: order.PaymentMethodID,
		Items:           items,
		CreatedAt:       order.CreatedAt,
		UpdatedAt:       order.UpdatedAt,
	})
}

func writeDomainError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrOrderNotFound):
		httpx.WriteError(ctx, w, http.StatusNotFound, "order_not_found", "order not found", nil)
	case errors.Is(err, app.ErrUnknownSKU):
		httpx.WriteError(ctx, w, http.StatusUnprocessableEntity, "unknown_sku", err.Error(), nil)
	case errors.Is(err, domain.ErrMissingCustomer),
		errors.Is(err, domain.ErrMissingPaymentMethod),
		errors.Is(err, domain.ErrNoItems),
		errors.Is(err, domain.ErrInvalidQuantity):
		httpx.WriteError(ctx, w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
	default:
		httpx.WriteError(ctx, w, http.StatusInternalServerError, "internal_error", "internal error", nil)
	}
}
