//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

func migrations() fstest.MapFS {
	return fstest.MapFS{
		"00001_widgets.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (id text primary key);

-- +goose Down
DROP TABLE widgets;
`)},
	}
}

func TestMigrateAppliesMigrations(t *testing.T) {
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	if err := postgres.Migrate(ctx, pool, migrations()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'widgets')`,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !exists {
		t.Fatal("widgets table was not created")
	}
}

func TestMigrateIsSafeUnderConcurrentStartup(t *testing.T) {
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	// goose retries pg_try_advisory_lock on a five second interval, so each
	// loser waits a full interval before its next attempt. Three racers prove
	// the property; more only add wall clock.
	const replicas = 3
	errs := make(chan error, replicas)
	for range replicas {
		go func() { errs <- postgres.Migrate(ctx, pool, migrations()) }()
	}
	for range replicas {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Migrate: %v", err)
		}
	}

	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM goose_db_version WHERE version_id = 1`).Scan(&applied); err != nil {
		t.Fatalf("count applied: %v", err)
	}
	if applied != 1 {
		t.Errorf("migration recorded %d times, want 1", applied)
	}
}
