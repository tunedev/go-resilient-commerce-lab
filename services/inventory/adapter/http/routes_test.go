package http_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

// TestRegisterNamesTheSpanAfterTheRoutePattern proves Register wires every
// route through httpx.Route rather than registering on the mux directly.
// The chain reproduces httpx.NewServer's middleware order, which is what
// makes the test discriminate: RequestID rebuilds the request with
// r.WithContext, so the request the mux records its matched pattern on is
// not the one otelhttp inspects for automatic naming, leaving httpx.Route's
// own SetName as the only path to a route-named span.
func TestRegisterNamesTheSpanAfterTheRoutePattern(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	mux := newMux(&fakeStore{setStockItem: domain.Item{SKU: "playstation-5"}})
	handler := httpx.RequestLogging(logger)(httpx.Recovery(logger)(mux))
	handler = httpx.RequestID(handler)
	traced := otelhttp.NewHandler(handler, "http.server", otelhttp.WithTracerProvider(tp))

	req := httptest.NewRequest(http.MethodPut, "/inventory/items/playstation-5",
		strings.NewReader(`{"available_quantity":1}`))
	traced.ServeHTTP(httptest.NewRecorder(), req)

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(ended))
	}
	if got, want := ended[0].Name(), "PUT /inventory/items/{sku}"; got != want {
		t.Errorf("span name = %q, want %q", got, want)
	}
}
