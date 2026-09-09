// Package http adapts the inventory use cases to HTTP.
package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

// Handler serves the inventory endpoints.
type Handler struct {
	reserve *app.Reserve
	manage  *app.Manage
}

// NewHandler builds the handler.
func NewHandler(reserve *app.Reserve, manage *app.Manage) *Handler {
	return &Handler{reserve: reserve, manage: manage}
}

type setStockRequest struct {
	AvailableQuantity int `json:"available_quantity"`
}

type itemResponse struct {
	SKU               string `json:"sku"`
	AvailableQuantity int    `json:"available_quantity"`
	ReservedQuantity  int    `json:"reserved_quantity"`
	Version           int64  `json:"version"`
}

func itemResponseFrom(item domain.Item) itemResponse {
	return itemResponse{
		SKU:               item.SKU,
		AvailableQuantity: item.AvailableQuantity,
		ReservedQuantity:  item.ReservedQuantity,
		Version:           item.Version,
	}
}

// SetStock handles PUT /inventory/items/{sku}. It sets the sku's available
// quantity to the given absolute value, so it is safe to repeat with the same
// value; the version still advances on each call.
func (h *Handler) SetStock(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req setStockRequest
	if err := decodeStrict(r, &req); err != nil {
		httpx.WriteError(ctx, w, http.StatusBadRequest,
			"invalid_request", "the request body is not valid JSON", nil)
		return
	}
	if req.AvailableQuantity < 0 {
		httpx.WriteError(ctx, w, http.StatusBadRequest,
			"invalid_request", "available_quantity must not be negative", nil)
		return
	}

	item, err := h.manage.SetStock(ctx, r.PathValue("sku"), req.AvailableQuantity)
	if err != nil {
		writeDomainError(ctx, w, err)
		return
	}

	httpx.WriteJSON(ctx, w, http.StatusOK, itemResponseFrom(item))
}

type reserveRequest struct {
	OrderID        string `json:"order_id"`
	SKU            string `json:"sku"`
	Quantity       int    `json:"quantity"`
	IdempotencyKey string `json:"idempotency_key"`
}

type reservationResponse struct {
	ReservationID string    `json:"reservation_id"`
	OrderID       string    `json:"order_id"`
	SKU           string    `json:"sku"`
	Quantity      int       `json:"quantity"`
	Status        string    `json:"status"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func reservationResponseFrom(r domain.Reservation) reservationResponse {
	return reservationResponse{
		ReservationID: r.ID,
		OrderID:       r.OrderID,
		SKU:           r.SKU,
		Quantity:      r.Quantity,
		Status:        string(r.Status),
		ExpiresAt:     r.ExpiresAt,
	}
}

// Reserve handles POST /inventory/reservations. A fresh reservation returns
// 201. A repeat carrying a previously used idempotency_key returns 200 with
// the reservation that key already produced: the use case never populates
// CreatedAt on a reservation it just created, only on one it read back from
// storage, so that field distinguishes the two cases here.
func (h *Handler) Reserve(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req reserveRequest
	if err := decodeStrict(r, &req); err != nil {
		httpx.WriteError(ctx, w, http.StatusBadRequest,
			"invalid_request", "the request body is not valid JSON", nil)
		return
	}

	reservation, err := h.reserve.Execute(ctx, app.ReserveCommand{
		OrderID:        req.OrderID,
		SKU:            req.SKU,
		Quantity:       req.Quantity,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		if errors.Is(err, domain.ErrInsufficientStock) {
			httpx.WriteError(ctx, w, http.StatusConflict, "insufficient_stock", err.Error(),
				map[string]any{"sku": req.SKU, "requested_quantity": req.Quantity})
			return
		}
		writeDomainError(ctx, w, err)
		return
	}

	status := http.StatusCreated
	if !reservation.CreatedAt.IsZero() {
		status = http.StatusOK
	}
	httpx.WriteJSON(ctx, w, status, reservationResponseFrom(reservation))
}

// Commit handles POST /inventory/reservations/{id}/commit.
func (h *Handler) Commit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	reservation, err := h.manage.Commit(ctx, r.PathValue("id"))
	if err != nil {
		writeDomainError(ctx, w, err)
		return
	}

	httpx.WriteJSON(ctx, w, http.StatusOK, reservationResponseFrom(reservation))
}

// Release handles POST /inventory/reservations/{id}/release.
func (h *Handler) Release(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	reservation, err := h.manage.Release(ctx, r.PathValue("id"))
	if err != nil {
		writeDomainError(ctx, w, err)
		return
	}

	httpx.WriteJSON(ctx, w, http.StatusOK, reservationResponseFrom(reservation))
}

// decodeStrict decodes r's JSON body into v, rejecting unknown fields.
func decodeStrict(r *http.Request, v any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}

// writeDomainError maps a use case error to the shared error envelope. It
// never echoes err's message for an error not in the table below, so an
// internal detail cannot leak into a response.
//
// domain.ErrInsufficientStock has no case here: only Reserve's use case
// produces it, and Reserve intercepts it before calling this function to
// attach the sku and requested quantity as details.
func writeDomainError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrMissingOrderID),
		errors.Is(err, domain.ErrMissingSKU),
		errors.Is(err, domain.ErrMissingKey),
		errors.Is(err, domain.ErrInvalidQuantity):
		httpx.WriteError(ctx, w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
	case errors.Is(err, domain.ErrIllegalTransition):
		httpx.WriteError(ctx, w, http.StatusConflict, "illegal_transition", err.Error(), nil)
	case errors.Is(err, domain.ErrReservationNotFound):
		httpx.WriteError(ctx, w, http.StatusNotFound, "reservation_not_found", err.Error(), nil)
	default:
		httpx.WriteError(ctx, w, http.StatusInternalServerError, "internal_error", "internal error", nil)
	}
}
