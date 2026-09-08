//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

func setup(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE widgets (id text primary key)`); err != nil {
		t.Fatalf("create widgets: %v", err)
	}
	if _, err := pool.Exec(ctx, outbox.Schema); err != nil {
		t.Fatalf("create outbox_events: %v", err)
	}
	return ctx, pool
}

func TestAppendCommitsWithTheStateChange(t *testing.T) {
	ctx, pool := setup(t)

	event, err := outbox.NewEvent("order", "ord_1", "OrderCreated", map[string]string{"order_id": "ord_1"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO widgets (id) VALUES ('ord_1')`); err != nil {
			return err
		}
		return outbox.Append(ctx, tx, event)
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	var widgets, events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&widgets); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if widgets != 1 || events != 1 {
		t.Fatalf("widgets = %d, events = %d, want 1 and 1", widgets, events)
	}
}

func TestAppendIsRolledBackWithTheStateChange(t *testing.T) {
	ctx, pool := setup(t)

	event, err := outbox.NewEvent("order", "ord_1", "OrderCreated", map[string]string{"order_id": "ord_1"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	sentinel := errors.New("business rule violated")
	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO widgets (id) VALUES ('ord_1')`); err != nil {
			return err
		}
		if err := outbox.Append(ctx, tx, event); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}

	var widgets, events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&widgets); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if widgets != 0 || events != 0 {
		t.Fatalf("widgets = %d, events = %d after rollback, want 0 and 0", widgets, events)
	}
}
