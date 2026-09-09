//go:build integration

package idempotency_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

func store(t *testing.T) (context.Context, *pgxpool.Pool, *idempotency.Store) {
	t.Helper()
	ctx := context.Background()
	pool := postgres.StartPostgres(t)
	if _, err := pool.Exec(ctx, idempotency.Schema); err != nil {
		t.Fatalf("create idempotency_keys: %v", err)
	}
	return ctx, pool, idempotency.NewStore(pool)
}

func TestClaimOwnsAFreshKey(t *testing.T) {
	ctx, _, s := store(t)

	outcome, _, err := s.Claim(ctx, "key-1", "hash-a")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if outcome != idempotency.OutcomeOwned {
		t.Errorf("outcome = %v, want OutcomeOwned", outcome)
	}
}

func TestSecondClaimWhileInProgress(t *testing.T) {
	ctx, _, s := store(t)

	if _, _, err := s.Claim(ctx, "key-1", "hash-a"); err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	outcome, _, err := s.Claim(ctx, "key-1", "hash-a")
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if outcome != idempotency.OutcomeInProgress {
		t.Errorf("outcome = %v, want OutcomeInProgress", outcome)
	}
}

func TestClaimWithDifferentHashConflicts(t *testing.T) {
	ctx, _, s := store(t)

	if _, _, err := s.Claim(ctx, "key-1", "hash-a"); err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	outcome, _, err := s.Claim(ctx, "key-1", "hash-b")
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if outcome != idempotency.OutcomeConflict {
		t.Errorf("outcome = %v, want OutcomeConflict", outcome)
	}
}

func TestClaimAfterCompletionReplays(t *testing.T) {
	ctx, pool, s := store(t)
	body := []byte(`{"order_id":"ord_1","status":"pending"}`)

	if _, _, err := s.Claim(ctx, "key-1", "hash-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return idempotency.Complete(ctx, tx, idempotency.Completion{
			Key:          "key-1",
			ResourceType: "order",
			ResourceID:   "ord_1",
			StatusCode:   http.StatusAccepted,
			ResponseBody: body,
		})
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	outcome, rec, err := s.Claim(ctx, "key-1", "hash-a")
	if err != nil {
		t.Fatalf("Claim after completion: %v", err)
	}
	if outcome != idempotency.OutcomeReplay {
		t.Fatalf("outcome = %v, want OutcomeReplay", outcome)
	}
	if rec.StatusCode != http.StatusAccepted {
		t.Errorf("StatusCode = %d, want 202", rec.StatusCode)
	}
	if string(rec.ResponseBody) != string(body) {
		t.Errorf("ResponseBody = %s, want %s", rec.ResponseBody, body)
	}
}

func TestReleaseAllowsRetry(t *testing.T) {
	ctx, _, s := store(t)

	if _, _, err := s.Claim(ctx, "key-1", "hash-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := s.Release(ctx, "key-1"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	outcome, _, err := s.Claim(ctx, "key-1", "hash-a")
	if err != nil {
		t.Fatalf("Claim after release: %v", err)
	}
	if outcome != idempotency.OutcomeOwned {
		t.Errorf("outcome = %v, want OutcomeOwned", outcome)
	}
}

func TestCompleteRefusesAnUnclaimedKey(t *testing.T) {
	ctx, pool, _ := store(t)

	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return idempotency.Complete(ctx, tx, idempotency.Completion{
			Key: "never-claimed", ResourceType: "order", ResourceID: "ord_1", StatusCode: 202,
			ResponseBody: []byte(`{}`),
		})
	})
	if err == nil {
		t.Fatal("Complete succeeded for a key that was never claimed")
	}
}

func TestConcurrentClaimsElectOneOwner(t *testing.T) {
	ctx, _, s := store(t)

	const racers = 16
	outcomes := make(chan idempotency.Outcome, racers)
	for range racers {
		go func() {
			outcome, _, err := s.Claim(ctx, "key-1", "hash-a")
			if err != nil {
				outcomes <- idempotency.OutcomeConflict
				return
			}
			outcomes <- outcome
		}()
	}

	owners := 0
	for range racers {
		if <-outcomes == idempotency.OutcomeOwned {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("%d goroutines claimed ownership, want exactly 1", owners)
	}
}
