package http

import (
	"net/http"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
)

// Register wires the inventory routes. There is no idempotency middleware
// here: Reserve implements its own idempotency through the idempotency_key
// column, and the other three endpoints are naturally safe to repeat.
func Register(mux *http.ServeMux, h *Handler) {
	httpx.Route(mux, "PUT /inventory/items/{sku}", http.HandlerFunc(h.SetStock))
	httpx.Route(mux, "POST /inventory/reservations", http.HandlerFunc(h.Reserve))
	httpx.Route(mux, "POST /inventory/reservations/{id}/commit", http.HandlerFunc(h.Commit))
	httpx.Route(mux, "POST /inventory/reservations/{id}/release", http.HandlerFunc(h.Release))
}
