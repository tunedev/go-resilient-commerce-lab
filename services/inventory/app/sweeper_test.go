//go:build integration

package app_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/logger"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	infrapg "github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
	inventorypg "github.com/tunedev/go-resilient-commerce-lab/services/inventory/adapter/postgres"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/migrations"
)

func newSweeperStore(t *testing.T) (context.Context, *pgxpool.Pool, *inventorypg.Store) {
	t.Helper()
	ctx := context.Background()
	pool := infrapg.StartPostgres(t)
	if err := infrapg.Migrate(ctx, pool, migrations.FS()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return ctx, pool, inventorypg.NewStore(pool)
}

func silentLogger() *slog.Logger {
	return logger.New(&bytes.Buffer{}, "test", slog.LevelError)
}

// reserveDue reserves quantity units of sku, expiring ttl from now. A
// negative ttl produces a reservation already due for sweeping.
func reserveDue(t *testing.T, ctx context.Context, store *inventorypg.Store, id, sku string, quantity int, ttl time.Duration) domain.Reservation {
	t.Helper()
	r := domain.Reservation{
		ID: id, OrderID: "ord_" + id, SKU: sku, Quantity: quantity,
		Status: domain.StatusReserved, ExpiresAt: time.Now().Add(ttl), IdempotencyKey: "key_" + id,
	}
	event, err := outbox.NewEvent("reservation", r.ID, "InventoryReserved", map[string]string{})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if err := store.Reserve(ctx, r, []outbox.Event{event}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	return r
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}

func reservationStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, id string) string {
	t.Helper()
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM inventory_reservations WHERE id = $1`, id).Scan(&status); err != nil {
		t.Fatalf("select status %s: %v", id, err)
	}
	return status
}

// TestSweeperExpiresADueReservationAndReturnsStock catches a sweeper that
// never calls ExpireDue, or that calls it but the store fails to credit
// available_quantity back.
func TestSweeperExpiresADueReservationAndReturnsStock(t *testing.T) {
	ctx, pool, store := newSweeperStore(t)
	if _, err := store.SetStock(ctx, "sku-sweep-1", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	r := reserveDue(t, ctx, store, "resv_sweep_1", "sku-sweep-1", 4, -time.Minute)

	sweeper := app.NewSweeper(store, silentLogger(), app.SweeperOptions{Interval: 10 * time.Millisecond, BatchSize: 10})
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(runCtx) }()

	waitFor(t, func() bool { return reservationStatus(t, ctx, pool, r.ID) == string(domain.StatusExpired) })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	var available int
	if err := pool.QueryRow(ctx, `SELECT available_quantity FROM inventory_items WHERE sku = $1`, "sku-sweep-1").Scan(&available); err != nil {
		t.Fatalf("select item: %v", err)
	}
	if available != 10 {
		t.Errorf("AvailableQuantity = %d, want 10 (fully returned)", available)
	}
}

// TestSweeperLeavesACommittedReservationAlone catches a sweeper whose store
// query claims by expires_at alone, without the "status = 'reserved'" guard,
// which would expire a committed reservation and double-credit its stock.
func TestSweeperLeavesACommittedReservationAlone(t *testing.T) {
	ctx, pool, store := newSweeperStore(t)
	if _, err := store.SetStock(ctx, "sku-sweep-2", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	r := reserveDue(t, ctx, store, "resv_sweep_2", "sku-sweep-2", 4, time.Hour)

	commitEvent, err := outbox.NewEvent("reservation", r.ID, "InventoryReservationCommitted", map[string]string{})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if _, err := store.Commit(ctx, r.ID, []outbox.Event{commitEvent}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE inventory_reservations SET expires_at = now() - interval '1 hour' WHERE id = $1`, r.ID); err != nil {
		t.Fatalf("force expiry: %v", err)
	}

	sweeper := app.NewSweeper(store, silentLogger(), app.SweeperOptions{Interval: 10 * time.Millisecond, BatchSize: 10})
	runCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(runCtx) }()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}

	if status := reservationStatus(t, ctx, pool, r.ID); status != string(domain.StatusCommitted) {
		t.Errorf("status = %q, want committed (never swept, whatever the clock says)", status)
	}

	var available, reserved int
	if err := pool.QueryRow(ctx, `SELECT available_quantity, reserved_quantity FROM inventory_items WHERE sku = $1`, "sku-sweep-2").
		Scan(&available, &reserved); err != nil {
		t.Fatalf("select item: %v", err)
	}
	if available != 6 || reserved != 0 {
		t.Errorf("item = (available %d, reserved %d), want (6, 0) unchanged by the sweep", available, reserved)
	}
}

// TestSweeperWritesAnExpiredEventPerExpiry catches a sweeper that expires
// rows but never calls buildEvent, or calls it with the wrong event type.
func TestSweeperWritesAnExpiredEventPerExpiry(t *testing.T) {
	ctx, pool, store := newSweeperStore(t)
	if _, err := store.SetStock(ctx, "sku-sweep-3", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	reserveDue(t, ctx, store, "resv_sweep_3a", "sku-sweep-3", 2, -time.Minute)
	reserveDue(t, ctx, store, "resv_sweep_3b", "sku-sweep-3", 3, -time.Minute)

	sweeper := app.NewSweeper(store, silentLogger(), app.SweeperOptions{Interval: 10 * time.Millisecond, BatchSize: 10})
	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(runCtx) }()

	waitFor(t, func() bool {
		var count int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM outbox_events WHERE event_type = 'InventoryReservationExpired'`,
		).Scan(&count); err != nil {
			return false
		}
		return count == 2
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestSweeperRunReturnsPromptlyAfterCancellation uses an interval far longer
// than the test's deadline, so the only way Run can return in time is by
// reacting to ctx.Done() rather than waiting on the ticker. Removing the
// ctx.Done() case from Run's select makes this test time out.
func TestSweeperRunReturnsPromptlyAfterCancellation(t *testing.T) {
	ctx, _, store := newSweeperStore(t)
	sweeper := app.NewSweeper(store, silentLogger(), app.SweeperOptions{Interval: time.Hour, BatchSize: 10})

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(runCtx) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Run did not return within 500ms of context cancellation")
	}
}

// flakyStore fails its first remainingFailures calls to ExpireDue, then
// delegates to the wrapped store.
type flakyStore struct {
	app.InventoryStore
	remainingFailures int
	calls             int
}

func (f *flakyStore) ExpireDue(ctx context.Context, limit int, buildEvent func(domain.Reservation) (outbox.Event, error)) (int, error) {
	f.calls++
	if f.remainingFailures > 0 {
		f.remainingFailures--
		return 0, errors.New("simulated transient database error")
	}
	return f.InventoryStore.ExpireDue(ctx, limit, buildEvent)
}

// TestSweeperRunSurvivesAFailingBatch proves a batch error does not stop the
// loop: if Run instead returned the error, done would deliver it before the
// reservation ever expires, and the wait below would time out.
func TestSweeperRunSurvivesAFailingBatch(t *testing.T) {
	ctx, pool, store := newSweeperStore(t)
	if _, err := store.SetStock(ctx, "sku-sweep-4", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	r := reserveDue(t, ctx, store, "resv_sweep_4", "sku-sweep-4", 5, -time.Minute)

	flaky := &flakyStore{InventoryStore: store, remainingFailures: 2}
	sweeper := app.NewSweeper(flaky, silentLogger(), app.SweeperOptions{Interval: 10 * time.Millisecond, BatchSize: 10})

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sweeper.Run(runCtx) }()

	waitFor(t, func() bool { return reservationStatus(t, ctx, pool, r.ID) == string(domain.StatusExpired) })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if flaky.calls < 3 {
		t.Errorf("ExpireDue called %d times, want at least 3 (2 failures then a success)", flaky.calls)
	}
}
