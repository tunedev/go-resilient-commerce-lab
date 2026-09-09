//go:build integration

package migrations_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/migrations"
)

func migrate(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool := postgres.StartPostgres(t)
	if err := postgres.Migrate(ctx, pool, migrations.FS()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return ctx, pool
}

func tableExists(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`,
		table,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("query information_schema.tables: %v", err)
	}
	return exists
}

func TestMigrationsApply(t *testing.T) {
	ctx, pool := migrate(t)

	if !tableExists(t, ctx, pool, "inventory_items") {
		t.Error("inventory_items table was not created")
	}
	if !tableExists(t, ctx, pool, "inventory_reservations") {
		t.Error("inventory_reservations table was not created")
	}
}

func TestNegativeStockIsRejected(t *testing.T) {
	ctx, pool := migrate(t)

	_, err := pool.Exec(ctx,
		`INSERT INTO inventory_items (sku, available_quantity) VALUES ('playstation-5', 10)`,
	)
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}

	_, err = pool.Exec(ctx,
		`UPDATE inventory_items SET available_quantity = -1 WHERE sku = 'playstation-5'`,
	)
	if err == nil {
		t.Fatal("UPDATE drove available_quantity negative, want a CHECK violation")
	}
}

func TestNegativeReservedQuantityIsRejected(t *testing.T) {
	ctx, pool := migrate(t)

	_, err := pool.Exec(ctx,
		`INSERT INTO inventory_items (sku, available_quantity) VALUES ('playstation-5', 10)`,
	)
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}

	_, err = pool.Exec(ctx,
		`UPDATE inventory_items SET reserved_quantity = -1 WHERE sku = 'playstation-5'`,
	)
	if err == nil {
		t.Fatal("UPDATE drove reserved_quantity negative, want a CHECK violation")
	}
}

func TestDuplicateIdempotencyKeyIsRejected(t *testing.T) {
	ctx, pool := migrate(t)

	_, err := pool.Exec(ctx,
		`INSERT INTO inventory_items (sku, available_quantity) VALUES ('playstation-5', 10)`,
	)
	if err != nil {
		t.Fatalf("insert item: %v", err)
	}

	insertReservation := `
		INSERT INTO inventory_reservations
			(id, order_id, sku, quantity, status, expires_at, idempotency_key)
		VALUES
			($1, 'order-1', 'playstation-5', 1, 'reserved', now() + interval '15 minutes', 'idem-1')`

	if _, err := pool.Exec(ctx, insertReservation, "res-1"); err != nil {
		t.Fatalf("insert first reservation: %v", err)
	}

	_, err = pool.Exec(ctx, insertReservation, "res-2")
	if err == nil {
		t.Fatal("second reservation with the same idempotency key succeeded, want a unique violation")
	}
}
