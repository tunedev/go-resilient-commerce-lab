//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	infrapg "github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/migrations"
)

// TestExpireOneDoesNotDoubleCreditAnAlreadyExpiredRow reproduces, without
// relying on goroutine timing, what a second sweeper would do if it read the
// due reservation before the first sweeper committed: call expireOne again
// with the same stale reservation data after the row is already 'expired'.
// A second live reservation on the same sku keeps reserved_quantity above
// zero throughout, so a wrong second credit does not trip the CHECK
// constraint and get masked by it - exactly the setup needed to observe the
// bug rather than have Postgres reject it outright.
//
// The real claim query's FOR UPDATE SKIP LOCKED decides whether a second
// sweeper blocks on a claimed row or moves on to a different one; it is not
// what stops two sweepers crediting the same row twice. Calling expireOne
// directly bypasses the claim to isolate the WHERE-clause guard that does:
// deleting the "AND status = 'reserved'" guard and the RowsAffected check in
// expireOne makes this test fail, with the second call double-crediting
// available_quantity and reporting success instead of finding zero rows.
func TestExpireOneDoesNotDoubleCreditAnAlreadyExpiredRow(t *testing.T) {
	ctx := context.Background()
	pool := infrapg.StartPostgres(t)
	if err := infrapg.Migrate(ctx, pool, migrations.FS()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	store := NewStore(pool)

	if _, err := store.SetStock(ctx, "sku-sweep", 20); err != nil {
		t.Fatalf("SetStock: %v", err)
	}

	due := domain.Reservation{
		ID: "resv_due", OrderID: "ord_due", SKU: "sku-sweep", Quantity: 4,
		Status: domain.StatusReserved, ExpiresAt: time.Now().Add(-time.Minute), IdempotencyKey: "key_due",
	}
	if err := reserve(ctx, store, due); err != nil {
		t.Fatalf("reserve due: %v", err)
	}

	live := domain.Reservation{
		ID: "resv_live", OrderID: "ord_live", SKU: "sku-sweep", Quantity: 5,
		Status: domain.StatusReserved, ExpiresAt: time.Now().Add(time.Hour), IdempotencyKey: "key_live",
	}
	if err := reserve(ctx, store, live); err != nil {
		t.Fatalf("reserve live: %v", err)
	}

	buildEvent := func(r domain.Reservation) (outbox.Event, error) {
		return outbox.NewEvent("reservation", r.ID, "InventoryReservationExpired", map[string]string{})
	}

	// First sweeper: legitimately expires due and credits its quantity back.
	ok, err := runExpireOne(ctx, pool, due, buildEvent)
	if err != nil {
		t.Fatalf("first expireOne: %v", err)
	}
	if !ok {
		t.Fatal("first expireOne found no rows to expire")
	}

	// Second sweeper: the same stale reservation data a concurrent claimant
	// would have read before the first sweeper committed. The row is now
	// already 'expired'.
	ok, err = runExpireOne(ctx, pool, due, buildEvent)
	if err != nil {
		t.Fatalf("second expireOne: %v", err)
	}
	if ok {
		t.Error("second expireOne reported success against an already-expired row")
	}

	var available, reserved int
	err = pool.QueryRow(ctx,
		`SELECT available_quantity, reserved_quantity FROM inventory_items WHERE sku = $1`, "sku-sweep",
	).Scan(&available, &reserved)
	if err != nil {
		t.Fatalf("select item: %v", err)
	}
	if available != 15 {
		t.Errorf("AvailableQuantity = %d, want 15 (20 stock - 5 held by live + 4 returned by the one legitimate expiry)", available)
	}
	if reserved != 5 {
		t.Errorf("ReservedQuantity = %d, want 5 (live's reservation, untouched)", reserved)
	}
}

func reserve(ctx context.Context, store *Store, r domain.Reservation) error {
	event, err := outbox.NewEvent("reservation", r.ID, "InventoryReserved", map[string]string{})
	if err != nil {
		return err
	}
	return store.Reserve(ctx, r, []outbox.Event{event})
}

func runExpireOne(ctx context.Context, pool *pgxpool.Pool, r domain.Reservation, buildEvent func(domain.Reservation) (outbox.Event, error)) (bool, error) {
	var ok bool
	err := infrapg.WithTx(ctx, pool, func(tx pgx.Tx) error {
		var err error
		ok, err = expireOne(ctx, tx, r, buildEvent)
		return err
	})
	return ok, err
}
