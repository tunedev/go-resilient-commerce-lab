package http

import (
	"net/http"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
)

// Register wires the order routes. Only POST /orders requires an
// Idempotency-Key; a read is already safe to repeat.
func Register(mux *http.ServeMux, h *Handler, store *idempotency.Store) {
	httpx.Route(mux, "POST /orders", idempotency.Require(store)(http.HandlerFunc(h.Create)))
	httpx.Route(mux, "GET /orders/{id}", http.HandlerFunc(h.Get))
}
