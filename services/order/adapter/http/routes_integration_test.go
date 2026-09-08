//go:build integration

package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	infrapg "github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
	orderhttp "github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/http"
	orderpg "github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/postgres"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/pricing"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/migrations"
)

// newRegisteredMux builds the real route table via Register, backed by a real
// Postgres-backed idempotency store, so the idempotency middleware and the
// order handler run composed exactly as they do in production.
func newRegisteredMux(t *testing.T) *http.ServeMux {
	t.Helper()
	ctx := context.Background()
	pool := infrapg.StartPostgres(t)
	if err := infrapg.Migrate(ctx, pool, migrations.FS()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	store := orderpg.NewStore(pool)
	handler := orderhttp.NewHandler(
		app.NewCreateOrder(store, pricing.NewStatic(), func() string { return "ord_" + uuid.NewString() }),
		app.NewGetOrder(store),
	)

	mux := http.NewServeMux()
	orderhttp.Register(mux, handler, idempotency.NewStore(pool))
	return mux
}

func doRequest(mux *http.ServeMux, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

const registeredValidBody = `{"customer_id":"cust_1","payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}`

func TestRegisterCreateOrderIsIdempotent(t *testing.T) {
	mux := newRegisteredMux(t)

	first := doRequest(mux, "key-1", registeredValidBody)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want 202: %s", first.Code, first.Body.String())
	}

	second := doRequest(mux, "key-1", registeredValidBody)
	if second.Code != first.Code {
		t.Fatalf("replay status = %d, want %d", second.Code, first.Code)
	}
	if second.Body.String() != first.Body.String() {
		t.Fatalf("replay body = %q, want %q", second.Body.String(), first.Body.String())
	}
}

func TestRegisterInvalidBodyReleasesTheClaim(t *testing.T) {
	mux := newRegisteredMux(t)

	invalidBody := `{"customer_id":"","payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}`
	rec := doRequest(mux, "key-invalid", invalidBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	retry := doRequest(mux, "key-invalid", registeredValidBody)
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry under the same key status = %d, want 202: %s; the claim was stranded",
			retry.Code, retry.Body.String())
	}
}
