//go:build integration

package idempotency_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

const requestBody = `{"customer_id":"cust_1","payment_method_id":"pm_ok"}`

// creator stands in for a handler that completes its idempotency claim inside
// its own transaction.
func creator(t *testing.T, pool *pgxpool.Pool, resourceID string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := idempotency.KeyFromContext(r.Context())
		if !ok {
			t.Error("handler ran without an idempotency key in context")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		received, err := io.ReadAll(r.Body)
		if err != nil || len(received) == 0 {
			t.Errorf("handler read %q from the request body, want the original payload", received)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		body := []byte(`{"order_id":"` + resourceID + `","status":"pending"}`)

		err = postgres.WithTx(r.Context(), pool, func(tx pgx.Tx) error {
			return idempotency.Complete(r.Context(), tx, idempotency.Completion{
				Key: key, ResourceType: "order", ResourceID: resourceID,
				StatusCode: http.StatusAccepted, ResponseBody: body,
			})
		})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(body)
	})
}

func post(t *testing.T, h http.Handler, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMiddlewareRejectsMissingKey(t *testing.T) {
	_, pool, s := store(t)
	h := idempotency.Require(s)(creator(t, pool, "ord_1"))

	rec := post(t, h, "", requestBody)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "idempotency_key_required") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestMiddlewareReplaysIdenticalRequest(t *testing.T) {
	_, pool, s := store(t)
	h := idempotency.Require(s)(creator(t, pool, "ord_1"))

	first := post(t, h, "key-1", requestBody)
	second := post(t, h, "key-1", requestBody)

	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want 202", first.Code)
	}
	if second.Code != first.Code {
		t.Errorf("replay status = %d, want %d", second.Code, first.Code)
	}
	if second.Body.String() != first.Body.String() {
		t.Errorf("replay body = %q, want %q", second.Body.String(), first.Body.String())
	}
}

func TestMiddlewareReplaysReorderedBody(t *testing.T) {
	_, pool, s := store(t)
	h := idempotency.Require(s)(creator(t, pool, "ord_1"))

	first := post(t, h, "key-1", requestBody)
	second := post(t, h, "key-1", `{"payment_method_id":"pm_ok","customer_id":"cust_1"}`)

	if second.Code != first.Code {
		t.Errorf("reordered body was treated as a conflict: status = %d", second.Code)
	}
}

func TestMiddlewareConflictsOnDifferentBody(t *testing.T) {
	_, pool, s := store(t)
	h := idempotency.Require(s)(creator(t, pool, "ord_1"))

	post(t, h, "key-1", requestBody)
	second := post(t, h, "key-1", `{"customer_id":"cust_2","payment_method_id":"pm_ok"}`)

	if second.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", second.Code)
	}
	if !strings.Contains(second.Body.String(), "idempotency_key_reused") {
		t.Errorf("body = %q", second.Body.String())
	}
}

func TestMiddlewareReportsInProgress(t *testing.T) {
	ctx, pool, s := store(t)
	if _, _, err := s.Claim(ctx, "key-1", mustHash(t, requestBody)); err != nil {
		t.Fatalf("pre-claim: %v", err)
	}

	h := idempotency.Require(s)(creator(t, pool, "ord_1"))
	rec := post(t, h, "key-1", requestBody)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "processing") {
		t.Errorf("body = %q, want the processing marker", rec.Body.String())
	}
}

func TestMiddlewareReleasesClaimOnHandlerFailure(t *testing.T) {
	_, _, s := store(t)
	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(r.Context(), w, http.StatusInternalServerError, "internal_error", "boom", nil)
	})
	h := idempotency.Require(s)(failing)

	if rec := post(t, h, "key-1", requestBody); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	outcome, _, err := s.Claim(context.Background(), "key-1", mustHash(t, requestBody))
	if err != nil {
		t.Fatalf("Claim after failure: %v", err)
	}
	if outcome != idempotency.OutcomeOwned {
		t.Fatalf("outcome = %v after a 500, want OutcomeOwned; the claim leaked", outcome)
	}
}

func TestMiddlewareReleasesClaimWhenHandlerPanics(t *testing.T) {
	_, _, s := store(t)
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := idempotency.Require(s)(panicking)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic did not propagate past the middleware")
			}
		}()
		post(t, h, "key-1", requestBody)
	}()

	outcome, _, err := s.Claim(context.Background(), "key-1", mustHash(t, requestBody))
	if err != nil {
		t.Fatalf("Claim after panic: %v", err)
	}
	if outcome != idempotency.OutcomeOwned {
		t.Fatalf("outcome = %v after a panic, want OutcomeOwned; the claim was stranded", outcome)
	}
}

func TestMiddlewareReleasesClaimOnClientError(t *testing.T) {
	_, _, s := store(t)
	rejecting := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(r.Context(), w, http.StatusBadRequest, "invalid_request", "bad", nil)
	})
	h := idempotency.Require(s)(rejecting)

	if rec := post(t, h, "key-1", requestBody); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}

	outcome, _, err := s.Claim(context.Background(), "key-1", mustHash(t, requestBody))
	if err != nil {
		t.Fatalf("Claim after 400: %v", err)
	}
	if outcome != idempotency.OutcomeOwned {
		t.Fatalf("outcome = %v after a 400, want OutcomeOwned; the claim was stranded", outcome)
	}
}

func mustHash(t *testing.T, body string) string {
	t.Helper()
	h, err := idempotency.Hash([]byte(body))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return h
}
