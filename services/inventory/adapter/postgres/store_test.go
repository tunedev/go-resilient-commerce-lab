//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	infrapg "github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
	inventorypg "github.com/tunedev/go-resilient-commerce-lab/services/inventory/adapter/postgres"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/migrations"
)

func newStore(t *testing.T) (context.Context, *pgxpool.Pool, *inventorypg.Store) {
	t.Helper()
	ctx := context.Background()
	pool := infrapg.StartPostgres(t)
	if err := infrapg.Migrate(ctx, pool, migrations.FS()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return ctx, pool, inventorypg.NewStore(pool)
}

func reservedEvent(t *testing.T, r domain.Reservation) outbox.Event {
	t.Helper()
	event, err := outbox.NewEvent("reservation", r.ID, "InventoryReserved", map[string]string{"reservation_id": r.ID})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	return event
}

func malformedEvent(aggregateID string) outbox.Event {
	return outbox.Event{
		ID:            uuid.New(),
		AggregateType: "reservation",
		AggregateID:   aggregateID,
		EventType:     "InventoryReserved",
		Payload:       json.RawMessage(`{not valid json`),
	}
}

func sampleReservation(sku string, quantity int) domain.Reservation {
	return domain.Reservation{
		ID:             "resv_" + sku,
		OrderID:        "ord_1",
		SKU:            sku,
		Quantity:       quantity,
		Status:         domain.StatusReserved,
		ExpiresAt:      time.Now().Add(time.Hour),
		IdempotencyKey: "key_" + sku,
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

func getItem(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sku string) domain.Item {
	t.Helper()
	var item domain.Item
	err := pool.QueryRow(ctx,
		`SELECT sku, available_quantity, reserved_quantity, version FROM inventory_items WHERE sku = $1`, sku,
	).Scan(&item.SKU, &item.AvailableQuantity, &item.ReservedQuantity, &item.Version)
	if err != nil {
		t.Fatalf("getItem %s: %v", sku, err)
	}
	return item
}

func TestReserveDecrementsAvailableAndWritesOutbox(t *testing.T) {
	ctx, pool, store := newStore(t)
	if _, err := store.SetStock(ctx, "sku-a", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}

	r := sampleReservation("sku-a", 3)
	if err := store.Reserve(ctx, r, []outbox.Event{reservedEvent(t, r)}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	item := getItem(t, ctx, pool, "sku-a")
	if item.AvailableQuantity != 7 {
		t.Errorf("AvailableQuantity = %d, want 7", item.AvailableQuantity)
	}
	if item.ReservedQuantity != 3 {
		t.Errorf("ReservedQuantity = %d, want 3", item.ReservedQuantity)
	}
	assertCount(t, ctx, pool, `SELECT count(*) FROM outbox_events WHERE aggregate_id = 'resv_sku-a'`, 1)
}

func TestReserveForMoreThanAvailableReturnsInsufficientStockAndChangesNothing(t *testing.T) {
	ctx, pool, store := newStore(t)
	if _, err := store.SetStock(ctx, "sku-b", 2); err != nil {
		t.Fatalf("SetStock: %v", err)
	}

	r := sampleReservation("sku-b", 5)
	err := store.Reserve(ctx, r, []outbox.Event{reservedEvent(t, r)})
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("err = %v, want ErrInsufficientStock", err)
	}

	item := getItem(t, ctx, pool, "sku-b")
	if item.AvailableQuantity != 2 || item.ReservedQuantity != 0 {
		t.Errorf("item = %+v, want unchanged (2, 0)", item)
	}
	assertCount(t, ctx, pool, `SELECT count(*) FROM inventory_reservations`, 0)
}

func TestReserveRollsBackEverythingWhenTheOutboxAppendFails(t *testing.T) {
	ctx, pool, store := newStore(t)
	if _, err := store.SetStock(ctx, "sku-c", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}

	r := sampleReservation("sku-c", 3)
	err := store.Reserve(ctx, r, []outbox.Event{malformedEvent(r.ID)})
	if err == nil {
		t.Fatal("Reserve succeeded with a malformed event")
	}

	item := getItem(t, ctx, pool, "sku-c")
	if item.AvailableQuantity != 10 || item.ReservedQuantity != 0 {
		t.Errorf("item = %+v, want unchanged (10, 0)", item)
	}
	assertCount(t, ctx, pool, `SELECT count(*) FROM inventory_reservations`, 0)
}

func TestReserveReturnsDuplicateKeyWhenIdempotencyKeyAlreadyClaimed(t *testing.T) {
	ctx, pool, store := newStore(t)
	if _, err := store.SetStock(ctx, "sku-dup", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}

	first := sampleReservation("sku-dup", 3)
	first.IdempotencyKey = "shared-key"
	if err := store.Reserve(ctx, first, []outbox.Event{reservedEvent(t, first)}); err != nil {
		t.Fatalf("Reserve first: %v", err)
	}

	second := sampleReservation("sku-dup", 3)
	second.ID = "resv_sku-dup-2"
	second.IdempotencyKey = "shared-key"
	err := store.Reserve(ctx, second, []outbox.Event{reservedEvent(t, second)})
	if !errors.Is(err, app.ErrDuplicateKey) {
		t.Fatalf("err = %v, want ErrDuplicateKey", err)
	}

	item := getItem(t, ctx, pool, "sku-dup")
	if item.AvailableQuantity != 7 {
		t.Errorf("AvailableQuantity = %d, want 7 (second attempt must not double-decrement)", item.AvailableQuantity)
	}
	assertCount(t, ctx, pool, `SELECT count(*) FROM inventory_reservations WHERE sku = 'sku-dup'`, 1)
}

// TestReserveDoesNotMaskAnIDCollisionAsDuplicateKey reuses a reservation id
// with a fresh idempotency key: a primary-key violation, not an
// idempotency-key replay. isUniqueViolation must not report ErrDuplicateKey
// for this - only matching the SQLSTATE, without also checking the
// constraint name, would.
func TestReserveDoesNotMaskAnIDCollisionAsDuplicateKey(t *testing.T) {
	ctx, pool, store := newStore(t)
	if _, err := store.SetStock(ctx, "sku-idcollide", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}

	first := sampleReservation("sku-idcollide", 3)
	if err := store.Reserve(ctx, first, []outbox.Event{reservedEvent(t, first)}); err != nil {
		t.Fatalf("Reserve first: %v", err)
	}

	second := sampleReservation("sku-idcollide", 3)
	second.IdempotencyKey = "a-fresh-key-not-shared-with-first"
	// second.ID is left equal to first.ID by sampleReservation.
	err := store.Reserve(ctx, second, []outbox.Event{reservedEvent(t, second)})
	if err == nil {
		t.Fatal("Reserve succeeded with a colliding id")
	}
	if errors.Is(err, app.ErrDuplicateKey) {
		t.Fatalf("err = %v, an id collision must not be reported as ErrDuplicateKey", err)
	}

	item := getItem(t, ctx, pool, "sku-idcollide")
	if item.AvailableQuantity != 7 {
		t.Errorf("AvailableQuantity = %d, want 7 (second attempt must not double-decrement)", item.AvailableQuantity)
	}
	assertCount(t, ctx, pool, `SELECT count(*) FROM inventory_reservations WHERE sku = 'sku-idcollide'`, 1)
}

func TestCommitMovesQuantityOutOfReservedAndLeavesAvailableAlone(t *testing.T) {
	ctx, pool, store := newStore(t)
	if _, err := store.SetStock(ctx, "sku-d", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	r := sampleReservation("sku-d", 4)
	if err := store.Reserve(ctx, r, []outbox.Event{reservedEvent(t, r)}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	event, err := outbox.NewEvent("reservation", r.ID, "InventoryReservationCommitted", map[string]string{"reservation_id": r.ID})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	got, err := store.Commit(ctx, r.ID, []outbox.Event{event})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got.Status != domain.StatusCommitted {
		t.Errorf("Status = %q, want committed", got.Status)
	}

	item := getItem(t, ctx, pool, "sku-d")
	if item.AvailableQuantity != 6 {
		t.Errorf("AvailableQuantity = %d, want 6 (unchanged by commit)", item.AvailableQuantity)
	}
	if item.ReservedQuantity != 0 {
		t.Errorf("ReservedQuantity = %d, want 0", item.ReservedQuantity)
	}
}

func TestReleaseReturnsQuantityToAvailable(t *testing.T) {
	ctx, pool, store := newStore(t)
	if _, err := store.SetStock(ctx, "sku-e", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	r := sampleReservation("sku-e", 4)
	if err := store.Reserve(ctx, r, []outbox.Event{reservedEvent(t, r)}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}

	event, err := outbox.NewEvent("reservation", r.ID, "InventoryReservationReleased", map[string]string{"reservation_id": r.ID})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	got, err := store.Release(ctx, r.ID, []outbox.Event{event})
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got.Status != domain.StatusReleased {
		t.Errorf("Status = %q, want released", got.Status)
	}

	item := getItem(t, ctx, pool, "sku-e")
	if item.AvailableQuantity != 10 || item.ReservedQuantity != 0 {
		t.Errorf("item = %+v, want (10, 0)", item)
	}
}

func TestReleaseOnACommittedReservationReturnsIllegalTransitionAndChangesNoStock(t *testing.T) {
	ctx, pool, store := newStore(t)
	if _, err := store.SetStock(ctx, "sku-f", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	r := sampleReservation("sku-f", 4)
	if err := store.Reserve(ctx, r, []outbox.Event{reservedEvent(t, r)}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	commitEvent, err := outbox.NewEvent("reservation", r.ID, "InventoryReservationCommitted", map[string]string{"reservation_id": r.ID})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if _, err := store.Commit(ctx, r.ID, []outbox.Event{commitEvent}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	releaseEvent, err := outbox.NewEvent("reservation", r.ID, "InventoryReservationReleased", map[string]string{"reservation_id": r.ID})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	_, err = store.Release(ctx, r.ID, []outbox.Event{releaseEvent})
	if !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}

	item := getItem(t, ctx, pool, "sku-f")
	if item.AvailableQuantity != 6 || item.ReservedQuantity != 0 {
		t.Errorf("item = %+v, want unchanged (6, 0)", item)
	}
}

func TestCommitOnUnknownIDReturnsReservationNotFound(t *testing.T) {
	ctx, _, store := newStore(t)
	event, err := outbox.NewEvent("reservation", "missing", "InventoryReservationCommitted", map[string]string{})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	_, err = store.Commit(ctx, "missing", []outbox.Event{event})
	if !errors.Is(err, domain.ErrReservationNotFound) {
		t.Fatalf("err = %v, want ErrReservationNotFound", err)
	}
}

func TestReleaseOnUnknownIDReturnsReservationNotFound(t *testing.T) {
	ctx, _, store := newStore(t)
	event, err := outbox.NewEvent("reservation", "missing", "InventoryReservationReleased", map[string]string{})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	_, err = store.Release(ctx, "missing", []outbox.Event{event})
	if !errors.Is(err, domain.ErrReservationNotFound) {
		t.Fatalf("err = %v, want ErrReservationNotFound", err)
	}
}

func TestSetStockCreatesThenOverwritesWithoutAccumulating(t *testing.T) {
	ctx, _, store := newStore(t)

	item, err := store.SetStock(ctx, "sku-g", 5)
	if err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	if item.AvailableQuantity != 5 || item.Version != 0 {
		t.Errorf("item = %+v, want (5, version 0)", item)
	}

	item, err = store.SetStock(ctx, "sku-g", 8)
	if err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	if item.AvailableQuantity != 8 {
		t.Errorf("AvailableQuantity = %d, want 8 (overwritten, not accumulated)", item.AvailableQuantity)
	}
	if item.Version != 1 {
		t.Errorf("Version = %d, want 1", item.Version)
	}
}

func TestExpireDueExpiresADueReservedReservationAndLeavesACommittedOneAlone(t *testing.T) {
	ctx, pool, store := newStore(t)
	if _, err := store.SetStock(ctx, "sku-h", 10); err != nil {
		t.Fatalf("SetStock: %v", err)
	}

	due := domain.Reservation{
		ID: "resv_due", OrderID: "ord_due", SKU: "sku-h", Quantity: 3,
		Status: domain.StatusReserved, ExpiresAt: time.Now().Add(-time.Minute), IdempotencyKey: "key_due",
	}
	if err := store.Reserve(ctx, due, []outbox.Event{reservedEvent(t, due)}); err != nil {
		t.Fatalf("Reserve due: %v", err)
	}

	committed := domain.Reservation{
		ID: "resv_committed", OrderID: "ord_committed", SKU: "sku-h", Quantity: 2,
		Status: domain.StatusReserved, ExpiresAt: time.Now().Add(-time.Minute), IdempotencyKey: "key_committed",
	}
	if err := store.Reserve(ctx, committed, []outbox.Event{reservedEvent(t, committed)}); err != nil {
		t.Fatalf("Reserve committed: %v", err)
	}
	commitEvent, err := outbox.NewEvent("reservation", committed.ID, "InventoryReservationCommitted", map[string]string{})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if _, err := store.Commit(ctx, committed.ID, []outbox.Event{commitEvent}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	buildEvent := func(r domain.Reservation) (outbox.Event, error) {
		return outbox.NewEvent("reservation", r.ID, "InventoryReservationExpired", map[string]string{"reservation_id": r.ID})
	}
	n, err := store.ExpireDue(ctx, 10, buildEvent)
	if err != nil {
		t.Fatalf("ExpireDue: %v", err)
	}
	if n != 1 {
		t.Fatalf("ExpireDue expired %d reservations, want 1", n)
	}

	var dueStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM inventory_reservations WHERE id = $1`, due.ID).Scan(&dueStatus); err != nil {
		t.Fatalf("select due status: %v", err)
	}
	if dueStatus != string(domain.StatusExpired) {
		t.Errorf("due status = %q, want expired", dueStatus)
	}

	var committedStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM inventory_reservations WHERE id = $1`, committed.ID).Scan(&committedStatus); err != nil {
		t.Fatalf("select committed status: %v", err)
	}
	if committedStatus != string(domain.StatusCommitted) {
		t.Errorf("committed status = %q, want committed (untouched by ExpireDue)", committedStatus)
	}

	item := getItem(t, ctx, pool, "sku-h")
	if item.AvailableQuantity != 8 {
		t.Errorf("AvailableQuantity = %d, want 8 (10 - 2 committed, 3 returned by expiry)", item.AvailableQuantity)
	}
	if item.ReservedQuantity != 0 {
		t.Errorf("ReservedQuantity = %d, want 0", item.ReservedQuantity)
	}

	assertCount(t, ctx, pool, `SELECT count(*) FROM outbox_events WHERE event_type = 'InventoryReservationExpired'`, 1)
}

// TestReserveIsSafeUnderConcurrentOverlappingRequests races more reservation
// attempts than available stock against the same sku. Exactly the number of
// units in stock succeed; the rest fail cleanly with ErrInsufficientStock,
// and the final row matches what succeeded. Removing the
// "available_quantity >= $2" guard from the UPDATE would let every concurrent
// request succeed until a later request drives available_quantity negative,
// which then fails the available_quantity >= 0 CHECK with a generic
// Postgres error instead of ErrInsufficientStock - this test's assertions
// (exact success count, and ErrInsufficientStock as the only failure mode)
// would then fail.
func TestReserveIsSafeUnderConcurrentOverlappingRequests(t *testing.T) {
	ctx, pool, store := newStore(t)
	const stock = 10
	const attempts = 30
	if _, err := store.SetStock(ctx, "sku-race", stock); err != nil {
		t.Fatalf("SetStock: %v", err)
	}

	// Built up front: reservedEvent calls t.Fatalf on failure, which must
	// only ever run on the test goroutine, not inside a racing one.
	reservations := make([]domain.Reservation, attempts)
	events := make([][]outbox.Event, attempts)
	for i := range attempts {
		r := domain.Reservation{
			ID: fmt.Sprintf("resv_race_%d", i), OrderID: fmt.Sprintf("ord_race_%d", i),
			SKU: "sku-race", Quantity: 1, Status: domain.StatusReserved,
			ExpiresAt: time.Now().Add(time.Hour), IdempotencyKey: fmt.Sprintf("key_race_%d", i),
		}
		reservations[i] = r
		events[i] = []outbox.Event{reservedEvent(t, r)}
	}

	var wg sync.WaitGroup
	results := make([]error, attempts)
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = store.Reserve(ctx, reservations[i], events[i])
		}(i)
	}
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

	if succeeded != stock {
		t.Errorf("succeeded = %d, want %d", succeeded, stock)
	}
	if insufficientStock != attempts-stock {
		t.Errorf("insufficientStock = %d, want %d", insufficientStock, attempts-stock)
	}
	if other != 0 {
		t.Errorf("other errors = %d, want 0", other)
	}

	item := getItem(t, ctx, pool, "sku-race")
	if item.AvailableQuantity != 0 {
		t.Errorf("AvailableQuantity = %d, want 0", item.AvailableQuantity)
	}
	if item.ReservedQuantity != stock {
		t.Errorf("ReservedQuantity = %d, want %d", item.ReservedQuantity, stock)
	}
	assertCount(t, ctx, pool, `SELECT count(*) FROM inventory_reservations WHERE status = 'reserved'`, stock)
}
