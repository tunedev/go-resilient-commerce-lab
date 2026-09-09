//go:build integration

package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	infrapg "github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
	inventoryhttp "github.com/tunedev/go-resilient-commerce-lab/services/inventory/adapter/http"
	inventorypg "github.com/tunedev/go-resilient-commerce-lab/services/inventory/adapter/postgres"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/migrations"
)

// newRegisteredMux builds the real route table via Register, backed by a real
// Postgres-backed store, so the handlers and the atomic stock guard run
// composed exactly as they do in production. It also returns the pool, for
// tests that need to warm connections or reach the database directly.
func newRegisteredMux(t *testing.T) (*http.ServeMux, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool := infrapg.StartPostgres(t)
	if err := infrapg.Migrate(ctx, pool, migrations.FS()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	store := inventorypg.NewStore(pool)
	handler := inventoryhttp.NewHandler(
		app.NewReserve(store, time.Hour, func() string { return "resv_" + uuid.NewString() }),
		app.NewManage(store),
	)

	mux := http.NewServeMux()
	inventoryhttp.Register(mux, handler)
	return mux, pool
}

// warmPool runs n concurrent queries so the pool ends up holding
// min(n, MaxConns) idle physical connections before a race begins. A cold
// pool spreads racing requests far enough apart in time that they never
// contend, which passes without ever exercising the race.
func warmPool(ctx context.Context, t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var one int
			if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
				t.Errorf("warmPool: %v", err)
			}
		}()
	}
	wg.Wait()
}

func doRequest(mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func reservationID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		ReservationID string `json:"reservation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return body.ReservationID
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

// TestRegisteredRoutesEnforceReservationLifecycle drives every registered
// route through a real ServeMux and a real Postgres store: one unit of stock
// cannot be reserved twice, a repeated idempotency key replays rather than
// reserving again, an insufficient-stock rejection leaves its idempotency key
// free for a later retry, and a committed reservation cannot be released
// through the API, matching the domain's terminal transition.
func TestRegisteredRoutesEnforceReservationLifecycle(t *testing.T) {
	mux, _ := newRegisteredMux(t)
	sku := "sku_" + uuid.NewString()

	setStock := doRequest(mux, http.MethodPut, "/inventory/items/"+sku, `{"available_quantity":1}`)
	if setStock.Code != http.StatusOK {
		t.Fatalf("SetStock status = %d, want 200: %s", setStock.Code, setStock.Body.String())
	}

	firstBody := `{"order_id":"order_1","sku":"` + sku + `","quantity":1,"idempotency_key":"key_1"}`
	first := doRequest(mux, http.MethodPost, "/inventory/reservations", firstBody)
	if first.Code != http.StatusCreated {
		t.Fatalf("first reserve status = %d, want 201: %s", first.Code, first.Body.String())
	}
	firstID := reservationID(t, first)

	replay := doRequest(mux, http.MethodPost, "/inventory/reservations", firstBody)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200: %s", replay.Code, replay.Body.String())
	}
	if got := reservationID(t, replay); got != firstID {
		t.Fatalf("replay reservation_id = %q, want %q", got, firstID)
	}

	secondBody := `{"order_id":"order_2","sku":"` + sku + `","quantity":1,"idempotency_key":"key_2"}`
	second := doRequest(mux, http.MethodPost, "/inventory/reservations", secondBody)
	if second.Code != http.StatusConflict {
		t.Fatalf("second reserve status = %d, want 409: %s", second.Code, second.Body.String())
	}
	if code := errorCode(t, second); code != "insufficient_stock" {
		t.Fatalf("second reserve code = %q, want insufficient_stock", code)
	}

	// An insufficient-stock rejection must not claim key_2 permanently: once
	// stock is available again, a retry under the same key must succeed.
	restock := doRequest(mux, http.MethodPut, "/inventory/items/"+sku, `{"available_quantity":1}`)
	if restock.Code != http.StatusOK {
		t.Fatalf("restock status = %d, want 200: %s", restock.Code, restock.Body.String())
	}
	retry := doRequest(mux, http.MethodPost, "/inventory/reservations", secondBody)
	if retry.Code != http.StatusCreated {
		t.Fatalf("retry under key_2 after restock status = %d, want 201: %s",
			retry.Code, retry.Body.String())
	}

	commit := doRequest(mux, http.MethodPost, "/inventory/reservations/"+firstID+"/commit", "")
	if commit.Code != http.StatusOK {
		t.Fatalf("commit status = %d, want 200: %s", commit.Code, commit.Body.String())
	}
	var committed struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(commit.Body.Bytes(), &committed); err != nil {
		t.Fatalf("unmarshal commit body: %v", err)
	}
	if committed.Status != "committed" {
		t.Fatalf("commit status field = %q, want committed", committed.Status)
	}

	release := doRequest(mux, http.MethodPost, "/inventory/reservations/"+firstID+"/release", "")
	if release.Code != http.StatusConflict {
		t.Fatalf("release status = %d, want 409: %s", release.Code, release.Body.String())
	}
	if code := errorCode(t, release); code != "illegal_transition" {
		t.Fatalf("release code = %q, want illegal_transition", code)
	}
}

// TestRegisteredRoutesReplayConcurrentRequestsSharingOneKeyForTheLastUnit
// reproduces a client whose POST timed out and retried under the same
// idempotency key while only one unit of stock remains. The stock guard in
// Store.Reserve is evaluated before the idempotency_key unique constraint, so
// a loser of the stock race that shares the winner's key must not be told
// insufficient_stock: it must be told it already won, via a replay.
func TestRegisteredRoutesReplayConcurrentRequestsSharingOneKeyForTheLastUnit(t *testing.T) {
	mux, pool := newRegisteredMux(t)
	sku := "sku_" + uuid.NewString()

	setStock := doRequest(mux, http.MethodPut, "/inventory/items/"+sku, `{"available_quantity":1}`)
	if setStock.Code != http.StatusOK {
		t.Fatalf("SetStock status = %d, want 200: %s", setStock.Code, setStock.Body.String())
	}

	const racers = 16
	ctx := context.Background()
	warmPool(ctx, t, pool, racers)

	body := `{"order_id":"order_1","sku":"` + sku + `","quantity":1,"idempotency_key":"key_shared"}`

	var ready, release sync.WaitGroup
	ready.Add(racers)
	release.Add(1)

	var wg sync.WaitGroup
	codes := make([]int, racers)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ready.Done()
			release.Wait()
			codes[i] = doRequest(mux, http.MethodPost, "/inventory/reservations", body).Code
		}(i)
	}
	ready.Wait()
	release.Done()
	wg.Wait()

	created, replayed, other := 0, 0, 0
	for _, code := range codes {
		switch code {
		case http.StatusCreated:
			created++
		case http.StatusOK:
			replayed++
		default:
			other++
		}
	}

	if created != 1 {
		t.Errorf("created (201) = %d, want 1", created)
	}
	if replayed != racers-1 {
		t.Errorf("replayed (200) = %d, want %d", replayed, racers-1)
	}
	if other != 0 {
		t.Errorf("other status codes = %d, want 0: a loser sharing the winner's key must replay, not fail", other)
	}
}
