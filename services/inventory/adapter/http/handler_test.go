package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	inventoryhttp "github.com/tunedev/go-resilient-commerce-lab/services/inventory/adapter/http"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

// fakeStore is a hand-written app.InventoryStore. Each method returns
// whatever the test configured and records what it was called with.
type fakeStore struct {
	byKeyResult domain.Reservation
	byKeyErr    error

	reserveErr error

	commitResult domain.Reservation
	commitErr    error

	releaseResult domain.Reservation
	releaseErr    error

	setStockItem domain.Item
	setStockErr  error

	appendedEvents []outbox.Event

	// sku and id capture what the handler passed through, so a test can prove
	// r.PathValue was populated by the mux rather than left empty.
	sku string
	id  string
}

func (f *fakeStore) Reserve(_ context.Context, _ domain.Reservation, _ []outbox.Event) error {
	return f.reserveErr
}

func (f *fakeStore) ReservationByKey(_ context.Context, _ string) (domain.Reservation, error) {
	if f.byKeyErr != nil {
		return domain.Reservation{}, f.byKeyErr
	}
	return f.byKeyResult, nil
}

func (f *fakeStore) Commit(_ context.Context, id string, _ []outbox.Event) (domain.Reservation, error) {
	f.id = id
	return f.commitResult, f.commitErr
}

func (f *fakeStore) Release(_ context.Context, id string, _ []outbox.Event) (domain.Reservation, error) {
	f.id = id
	return f.releaseResult, f.releaseErr
}

func (f *fakeStore) SetStock(_ context.Context, sku string, _ int) (domain.Item, error) {
	f.sku = sku
	return f.setStockItem, f.setStockErr
}

func (f *fakeStore) AppendEvents(_ context.Context, events []outbox.Event) error {
	f.appendedEvents = events
	return nil
}

func (f *fakeStore) ExpireDue(_ context.Context, _ int, _ func(domain.Reservation) (outbox.Event, error)) (int, error) {
	return 0, nil
}

// newMux registers the real routes over store, so PathValue is populated by
// ServeMux routing rather than left empty as a direct handler call would.
func newMux(store app.InventoryStore) *http.ServeMux {
	handler := inventoryhttp.NewHandler(
		app.NewReserve(store, time.Hour, func() string { return "resv_test" }),
		app.NewManage(store),
	)
	mux := http.NewServeMux()
	inventoryhttp.Register(mux, handler)
	return mux
}

func do(mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func errorBody(t *testing.T, rec *httptest.ResponseRecorder) (code string, details map[string]any) {
	t.Helper()
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code, body.Error.Details
}

func errorMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return body.Error.Message
}

const validReserveBody = `{"order_id":"order_1","sku":"playstation-5","quantity":2,"idempotency_key":"key_1"}`

func TestSetStockSucceeds(t *testing.T) {
	store := &fakeStore{setStockItem: domain.Item{
		SKU: "playstation-5", AvailableQuantity: 10, ReservedQuantity: 2, Version: 3,
	}}
	rec := do(newMux(store), http.MethodPut, "/inventory/items/playstation-5", `{"available_quantity":10}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if store.sku != "playstation-5" {
		t.Errorf("SetStock called with sku %q; PathValue did not resolve", store.sku)
	}

	var got struct {
		SKU               string `json:"sku"`
		AvailableQuantity int    `json:"available_quantity"`
		ReservedQuantity  int    `json:"reserved_quantity"`
		Version           int64  `json:"version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SKU != "playstation-5" || got.AvailableQuantity != 10 || got.ReservedQuantity != 2 || got.Version != 3 {
		t.Errorf("body = %+v", got)
	}
}

func TestSetStockRejectsNegativeQuantity(t *testing.T) {
	rec := do(newMux(&fakeStore{}), http.MethodPut, "/inventory/items/playstation-5", `{"available_quantity":-1}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if code, _ := errorBody(t, rec); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}
}

func TestSetStockRejectsMalformedJSON(t *testing.T) {
	rec := do(newMux(&fakeStore{}), http.MethodPut, "/inventory/items/playstation-5", `{"available_quantity":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestSetStockRejectsUnknownField(t *testing.T) {
	rec := do(newMux(&fakeStore{}), http.MethodPut, "/inventory/items/playstation-5",
		`{"available_quantity":10,"discount":"none"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; the decoder must disallow unknown fields", rec.Code)
	}
}

func TestReserveReturnsCreatedForANewReservation(t *testing.T) {
	rec := do(newMux(&fakeStore{byKeyErr: domain.ErrReservationNotFound}),
		http.MethodPost, "/inventory/reservations", validReserveBody)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}

	var got struct {
		ReservationID string `json:"reservation_id"`
		OrderID       string `json:"order_id"`
		SKU           string `json:"sku"`
		Quantity      int    `json:"quantity"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ReservationID != "resv_test" || got.OrderID != "order_1" || got.SKU != "playstation-5" ||
		got.Quantity != 2 || got.Status != string(domain.StatusReserved) {
		t.Errorf("body = %+v", got)
	}
}

func TestReserveReturnsOKForARepeatedKey(t *testing.T) {
	existing := domain.Reservation{
		ID: "resv_existing", OrderID: "order_1", SKU: "playstation-5",
		Quantity: 2, Status: domain.StatusReserved, CreatedAt: time.Now(),
	}
	rec := do(newMux(&fakeStore{byKeyResult: existing}),
		http.MethodPost, "/inventory/reservations", validReserveBody)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got struct {
		ReservationID string `json:"reservation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.ReservationID != "resv_existing" {
		t.Errorf("reservation_id = %q, want resv_existing", got.ReservationID)
	}
}

func TestReserveRejectsMissingOrderID(t *testing.T) {
	body := `{"sku":"playstation-5","quantity":2,"idempotency_key":"key_1"}`
	rec := do(newMux(&fakeStore{byKeyErr: domain.ErrReservationNotFound}),
		http.MethodPost, "/inventory/reservations", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if code, _ := errorBody(t, rec); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}
}

// TestReserveRejectsMalformedJSON asserts the decode-failure message
// specifically: a zero-valued request also fails domain validation with a
// 400, so the status code alone cannot tell a decode failure from a
// validation failure that happened to pass decoding.
func TestReserveRejectsMalformedJSON(t *testing.T) {
	rec := do(newMux(&fakeStore{}), http.MethodPost, "/inventory/reservations", `{"order_id":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	want := "the request body is not valid JSON"
	if msg := errorMessage(t, rec); msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}
}

func TestReserveRejectsUnknownField(t *testing.T) {
	body := `{"order_id":"order_1","sku":"playstation-5","quantity":2,"idempotency_key":"key_1","extra":"x"}`
	rec := do(newMux(&fakeStore{}), http.MethodPost, "/inventory/reservations", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; the decoder must disallow unknown fields", rec.Code)
	}
}

func TestReserveMapsInsufficientStockWithDetails(t *testing.T) {
	store := &fakeStore{byKeyErr: domain.ErrReservationNotFound, reserveErr: domain.ErrInsufficientStock}
	rec := do(newMux(store), http.MethodPost, "/inventory/reservations", validReserveBody)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	code, details := errorBody(t, rec)
	if code != "insufficient_stock" {
		t.Errorf("code = %q, want insufficient_stock", code)
	}
	if details["sku"] != "playstation-5" {
		t.Errorf("details.sku = %v, want playstation-5", details["sku"])
	}
	if details["requested_quantity"] != float64(2) {
		t.Errorf("details.requested_quantity = %v, want 2", details["requested_quantity"])
	}
}

func TestReserveMapsUnexpectedErrorToInternalErrorWithoutLeakingIt(t *testing.T) {
	store := &fakeStore{byKeyErr: domain.ErrReservationNotFound, reserveErr: errors.New("connection reset by peer")}
	rec := do(newMux(store), http.MethodPost, "/inventory/reservations", validReserveBody)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "connection reset") {
		t.Errorf("body = %q leaks the internal error", rec.Body.String())
	}
}

func TestCommitSucceeds(t *testing.T) {
	store := &fakeStore{commitResult: domain.Reservation{ID: "resv_1", Status: domain.StatusCommitted}}
	rec := do(newMux(store), http.MethodPost, "/inventory/reservations/resv_1/commit", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if store.id != "resv_1" {
		t.Errorf("Commit called with id %q; PathValue did not resolve", store.id)
	}

	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != string(domain.StatusCommitted) {
		t.Errorf("status = %q, want committed", got.Status)
	}
}

func TestCommitMapsIllegalTransition(t *testing.T) {
	rec := do(newMux(&fakeStore{commitErr: domain.ErrIllegalTransition}),
		http.MethodPost, "/inventory/reservations/resv_1/commit", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if code, _ := errorBody(t, rec); code != "illegal_transition" {
		t.Errorf("code = %q, want illegal_transition", code)
	}
}

func TestCommitMapsReservationNotFound(t *testing.T) {
	rec := do(newMux(&fakeStore{commitErr: domain.ErrReservationNotFound}),
		http.MethodPost, "/inventory/reservations/resv_missing/commit", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if code, _ := errorBody(t, rec); code != "reservation_not_found" {
		t.Errorf("code = %q, want reservation_not_found", code)
	}
}

func TestReleaseSucceeds(t *testing.T) {
	store := &fakeStore{releaseResult: domain.Reservation{ID: "resv_1", Status: domain.StatusReleased}}
	rec := do(newMux(store), http.MethodPost, "/inventory/reservations/resv_1/release", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if store.id != "resv_1" {
		t.Errorf("Release called with id %q; PathValue did not resolve", store.id)
	}

	var got struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != string(domain.StatusReleased) {
		t.Errorf("status = %q, want released", got.Status)
	}
}

func TestReleaseMapsIllegalTransition(t *testing.T) {
	rec := do(newMux(&fakeStore{releaseErr: domain.ErrIllegalTransition}),
		http.MethodPost, "/inventory/reservations/resv_1/release", "")

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if code, _ := errorBody(t, rec); code != "illegal_transition" {
		t.Errorf("code = %q, want illegal_transition", code)
	}
}
