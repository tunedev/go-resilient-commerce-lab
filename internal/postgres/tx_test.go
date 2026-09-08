//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

func TestWithTxCommitsOnSuccess(t *testing.T) {
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE widgets (id text primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO widgets (id) VALUES ('a')`)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE widgets (id text primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	sentinel := errors.New("business rule violated")
	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO widgets (id) VALUES ('a')`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx returned %v, want %v", err, sentinel)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d after rollback, want 0", count)
	}
}

func TestWithTxRollsBackOnPanic(t *testing.T) {
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE widgets (id text primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic did not propagate")
			}
		}()
		_ = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO widgets (id) VALUES ('a')`); err != nil {
				return err
			}
			panic("boom")
		})
	}()

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d after panic, want 0", count)
	}
}
