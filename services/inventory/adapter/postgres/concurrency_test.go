//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

// warmPool runs n concurrent queries so the pool ends up holding
// min(n, MaxConns) idle physical connections.
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

// TestConcurrentBuyersOfTheLastUnit races sixteen buyers with distinct
// idempotency keys against a single unit of stock. Distinct keys matter: with
// a shared key the losers would fail on deduplication rather than on stock,
// proving nothing about overselling.
//
// The pool is pre-warmed and the goroutines held at a barrier and released
// together, keeping connection setup out of the race window - otherwise the
// first goroutine to already hold a connection can finish before the rest
// have even connected, and the test passes without ever racing.
func TestConcurrentBuyersOfTheLastUnit(t *testing.T) {
	ctx, pool, store := newStore(t)
	const buyers = 16
	if _, err := store.SetStock(ctx, "sku-last-unit", 1); err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	warmPool(ctx, t, pool, buyers)

	reservations := make([]domain.Reservation, buyers)
	events := make([][]outbox.Event, buyers)
	for i := range buyers {
		r := domain.Reservation{
			ID: fmt.Sprintf("resv_last_%d", i), OrderID: fmt.Sprintf("ord_last_%d", i),
			SKU: "sku-last-unit", Quantity: 1, Status: domain.StatusReserved,
			ExpiresAt: time.Now().Add(time.Hour), IdempotencyKey: fmt.Sprintf("key_last_%d", i),
		}
		reservations[i] = r
		events[i] = []outbox.Event{reservedEvent(t, r)}
	}

	var ready, release sync.WaitGroup
	ready.Add(buyers)
	release.Add(1)

	var wg sync.WaitGroup
	results := make([]error, buyers)
	for i := range buyers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ready.Done()
			release.Wait()
			results[i] = store.Reserve(ctx, reservations[i], events[i])
		}(i)
	}

	ready.Wait()
	release.Done()
	wg.Wait()

	succeeded, insufficientStock, other := 0, 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, domain.ErrInsufficientStock):
			insufficientStock++
		default:
			other++
		}
	}

	if succeeded != 1 {
		t.Errorf("succeeded = %d, want 1", succeeded)
	}
	if insufficientStock != buyers-1 {
		t.Errorf("insufficientStock = %d, want %d", insufficientStock, buyers-1)
	}
	if other != 0 {
		t.Errorf("other errors = %d, want 0", other)
	}

	item := getItem(t, ctx, pool, "sku-last-unit")
	if item.AvailableQuantity != 0 {
		t.Errorf("AvailableQuantity = %d, want 0", item.AvailableQuantity)
	}
	if item.ReservedQuantity != 1 {
		t.Errorf("ReservedQuantity = %d, want 1", item.ReservedQuantity)
	}
	assertCount(t, ctx, pool, `SELECT count(*) FROM inventory_reservations`, 1)
	assertCount(t, ctx, pool, `SELECT count(*) FROM outbox_events`, 1)
}
