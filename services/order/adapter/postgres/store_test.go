//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	infrapg "github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
	orderpg "github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/postgres"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/migrations"
)

func newStore(t *testing.T) (context.Context, *pgxpool.Pool, *orderpg.Store) {
	t.Helper()
	ctx := context.Background()
	pool := infrapg.StartPostgres(t)
	if err := infrapg.Migrate(ctx, pool, migrations.FS()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return ctx, pool, orderpg.NewStore(pool)
}

func sampleOrder() domain.Order {
	return domain.Order{
		ID: "ord_1", CustomerID: "cust_1", Status: domain.StatusPending,
		TotalAmount: 150000, Currency: "NGN", PaymentMethodID: "pm_ok",
		Items: []domain.Item{{SKU: "playstation-5", Quantity: 1, UnitPrice: 150000}},
	}
}

func TestCreateOrderWritesOrderItemsOutboxAndCompletionAtomically(t *testing.T) {
	ctx, pool, store := newStore(t)

	if _, _, err := idempotency.NewStore(pool).Claim(ctx, "key-1", "hash-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	event, err := outbox.NewEvent("order", "ord_1", "OrderCreated", map[string]string{"order_id": "ord_1"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	err = store.CreateOrder(ctx, sampleOrder(), []outbox.Event{event}, &idempotency.Completion{
		Key: "key-1", ResourceType: "order", ResourceID: "ord_1",
		StatusCode: http.StatusAccepted, ResponseBody: []byte(`{"order_id":"ord_1"}`),
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	assertCount(t, ctx, pool, `SELECT count(*) FROM orders`, 1)
	assertCount(t, ctx, pool, `SELECT count(*) FROM order_items`, 1)
	assertCount(t, ctx, pool, `SELECT count(*) FROM outbox_events`, 1)
	assertCount(t, ctx, pool, `SELECT count(*) FROM idempotency_keys WHERE state = 'completed'`, 1)
}

func TestCreateOrderRollsBackEverythingWhenCompletionFails(t *testing.T) {
	ctx, pool, store := newStore(t)

	event, err := outbox.NewEvent("order", "ord_1", "OrderCreated", map[string]string{"order_id": "ord_1"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	// The key was never claimed, so Complete fails and must take the order with it.
	err = store.CreateOrder(ctx, sampleOrder(), []outbox.Event{event}, &idempotency.Completion{
		Key: "never-claimed", ResourceType: "order", ResourceID: "ord_1",
		StatusCode: http.StatusAccepted, ResponseBody: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("CreateOrder succeeded with an unclaimed key")
	}

	assertCount(t, ctx, pool, `SELECT count(*) FROM orders`, 0)
	assertCount(t, ctx, pool, `SELECT count(*) FROM order_items`, 0)
	assertCount(t, ctx, pool, `SELECT count(*) FROM outbox_events`, 0)
}

func TestGetOrderReturnsItems(t *testing.T) {
	ctx, _, store := newStore(t)

	if err := store.CreateOrder(ctx, sampleOrder(), nil, nil); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	got, err := store.GetOrder(ctx, "ord_1")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].SKU != "playstation-5" {
		t.Errorf("Items = %+v", got.Items)
	}
	if got.Status != domain.StatusPending {
		t.Errorf("Status = %q", got.Status)
	}
}

func TestGetOrderNotFound(t *testing.T) {
	ctx, _, store := newStore(t)

	_, err := store.GetOrder(ctx, "ord_missing")
	if !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("err = %v, want ErrOrderNotFound", err)
	}
}

func assertCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, query).Scan(&got); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if got != want {
		t.Errorf("%s = %d, want %d", query, got, want)
	}
}
