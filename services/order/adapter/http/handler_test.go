package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	orderhttp "github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/http"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

type stubStore struct {
	created domain.Order
	getErr  error
}

func (s *stubStore) CreateOrder(_ context.Context, o domain.Order, _ []outbox.Event, _ *idempotency.Completion) error {
	s.created = o
	return nil
}

func (s *stubStore) GetOrder(_ context.Context, id string) (domain.Order, error) {
	if s.getErr != nil {
		return domain.Order{}, s.getErr
	}
	return domain.Order{
		ID: id, CustomerID: "cust_1", Status: domain.StatusPending,
		TotalAmount: 150000, Currency: "NGN", PaymentMethodID: "pm_ok",
		Items: []domain.Item{{SKU: "playstation-5", Quantity: 1, UnitPrice: 150000}},
	}, nil
}

type stubPricer struct{ err error }

func (p stubPricer) UnitPrice(context.Context, string) (int64, error) {
	if p.err != nil {
		return 0, p.err
	}
	return 150000, nil
}

func (p stubPricer) Currency() string { return "NGN" }

func newMux(store app.OrderStore, pricer app.Pricer) *http.ServeMux {
	handler := orderhttp.NewHandler(
		app.NewCreateOrder(store, pricer, func() string { return "ord_test" }),
		app.NewGetOrder(store),
	)
	mux := http.NewServeMux()
	mux.Handle("POST /orders", http.HandlerFunc(handler.Create))
	mux.Handle("GET /orders/{id}", http.HandlerFunc(handler.Get))
	return mux
}

func do(mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

const validBody = `{"customer_id":"cust_1","payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}`

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

func TestCreateAccepted(t *testing.T) {
	store := &stubStore{}
	rec := do(newMux(store, stubPricer{}), http.MethodPost, "/orders", validBody)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	var got app.CreateOrderResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OrderID != "ord_test" || got.Status != string(domain.StatusPending) {
		t.Errorf("body = %+v", got)
	}
	if store.created.TotalAmount != 150000 {
		t.Errorf("persisted total = %d, want 150000", store.created.TotalAmount)
	}
}

func TestCreateRejectsMalformedJSON(t *testing.T) {
	rec := do(newMux(&stubStore{}, stubPricer{}), http.MethodPost, "/orders", `{"customer_id":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if code := errorCode(t, rec); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}
}

func TestCreateRejectsUnknownField(t *testing.T) {
	body := `{"customer_id":"cust_1","payment_method_id":"pm_ok","items":[],"discount":"none"}`
	rec := do(newMux(&stubStore{}, stubPricer{}), http.MethodPost, "/orders", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; the decoder must disallow unknown fields", rec.Code)
	}
}

func TestCreateRejectsMissingCustomer(t *testing.T) {
	body := `{"payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}`
	rec := do(newMux(&stubStore{}, stubPricer{}), http.MethodPost, "/orders", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if code := errorCode(t, rec); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}
}

func TestCreateRejectsUnknownSKU(t *testing.T) {
	mux := newMux(&stubStore{}, stubPricer{err: app.ErrUnknownSKU})
	rec := do(mux, http.MethodPost, "/orders", validBody)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if code := errorCode(t, rec); code != "unknown_sku" {
		t.Errorf("code = %q, want unknown_sku", code)
	}
}

func TestGetReturnsOrderWithItems(t *testing.T) {
	rec := do(newMux(&stubStore{}, stubPricer{}), http.MethodGet, "/orders/ord_test", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got struct {
		OrderID string `json:"order_id"`
		Status  string `json:"status"`
		Items   []struct {
			SKU       string `json:"sku"`
			UnitPrice int64  `json:"unit_price"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OrderID != "ord_test" {
		t.Errorf("order_id = %q; PathValue did not resolve", got.OrderID)
	}
	if len(got.Items) != 1 || got.Items[0].SKU != "playstation-5" {
		t.Errorf("items = %+v", got.Items)
	}
}

func TestGetNotFound(t *testing.T) {
	mux := newMux(&stubStore{getErr: domain.ErrOrderNotFound}, stubPricer{})
	rec := do(mux, http.MethodGet, "/orders/ord_missing", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if code := errorCode(t, rec); code != "order_not_found" {
		t.Errorf("code = %q, want order_not_found", code)
	}
}
