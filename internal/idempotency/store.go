package idempotency

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema is the idempotency_keys DDL. Each service that accepts an
// Idempotency-Key copies it into its own migrations.
const Schema = `
CREATE TABLE idempotency_keys (
    key           text PRIMARY KEY,
    request_hash  text        NOT NULL,
    state         text        NOT NULL,
    resource_type text        NOT NULL DEFAULT '',
    resource_id   text        NOT NULL DEFAULT '',
    response_body json,
    status_code   integer     NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT idempotency_keys_state_valid
        CHECK (state IN ('in_progress', 'completed'))
);
`

// Key states.
const (
	StateInProgress = "in_progress"
	StateCompleted  = "completed"
)

// Outcome is what a Claim decided.
type Outcome int

// Claim outcomes.
const (
	// OutcomeOwned means this request holds the key and should run.
	OutcomeOwned Outcome = iota
	// OutcomeReplay means the key completed; return the stored response.
	OutcomeReplay
	// OutcomeInProgress means another request holds the key and has not finished.
	OutcomeInProgress
	// OutcomeConflict means the key was used with a different request body.
	OutcomeConflict
)

// Record is a stored idempotency key.
type Record struct {
	Key          string
	RequestHash  string
	State        string
	ResourceType string
	ResourceID   string
	ResponseBody []byte
	StatusCode   int
}

// Store reads and writes idempotency keys.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Claim attempts to take ownership of key. Exactly one concurrent caller
// receives OutcomeOwned; the insert-or-nothing is what elects the owner.
func (s *Store) Claim(ctx context.Context, key, requestHash string) (Outcome, Record, error) {
	const insert = `
INSERT INTO idempotency_keys (key, request_hash, state)
VALUES ($1, $2, 'in_progress')
ON CONFLICT (key) DO NOTHING
RETURNING key`

	var claimed string
	err := s.pool.QueryRow(ctx, insert, key, requestHash).Scan(&claimed)
	switch {
	case err == nil:
		return OutcomeOwned, Record{Key: key, RequestHash: requestHash, State: StateInProgress}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return 0, Record{}, fmt.Errorf("idempotency: claim: %w", err)
	}

	existing, err := s.get(ctx, key)
	if err != nil {
		return 0, Record{}, err
	}
	if existing.RequestHash != requestHash {
		return OutcomeConflict, existing, nil
	}
	if existing.State == StateCompleted {
		return OutcomeReplay, existing, nil
	}
	return OutcomeInProgress, existing, nil
}

func (s *Store) get(ctx context.Context, key string) (Record, error) {
	const query = `
SELECT key, request_hash, state, resource_type, resource_id,
       coalesce(response_body, 'null'::json), status_code
  FROM idempotency_keys
 WHERE key = $1`

	var r Record
	err := s.pool.QueryRow(ctx, query, key).Scan(
		&r.Key, &r.RequestHash, &r.State, &r.ResourceType,
		&r.ResourceID, &r.ResponseBody, &r.StatusCode,
	)
	if err != nil {
		return Record{}, fmt.Errorf("idempotency: get %s: %w", key, err)
	}
	return r, nil
}

// Release drops an in-progress claim so the request can be retried. It never
// removes a completed key.
func (s *Store) Release(ctx context.Context, key string) error {
	const query = `DELETE FROM idempotency_keys WHERE key = $1 AND state = 'in_progress'`

	if _, err := s.pool.Exec(ctx, query, key); err != nil {
		return fmt.Errorf("idempotency: release %s: %w", key, err)
	}
	return nil
}

// Completion is the result recorded against a claimed key.
type Completion struct {
	Key          string
	ResourceType string
	ResourceID   string
	StatusCode   int
	ResponseBody []byte
}

// Complete flips a claimed key to completed inside the caller's transaction,
// so the key and the resource it names commit together.
func Complete(ctx context.Context, tx pgx.Tx, c Completion) error {
	const query = `
UPDATE idempotency_keys
   SET state = 'completed', resource_type = $2, resource_id = $3,
       status_code = $4, response_body = $5
 WHERE key = $1 AND state = 'in_progress'`

	tag, err := tx.Exec(ctx, query, c.Key, c.ResourceType, c.ResourceID, c.StatusCode, c.ResponseBody)
	if err != nil {
		return fmt.Errorf("idempotency: complete %s: %w", c.Key, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("idempotency: complete %s: no in-progress claim", c.Key)
	}
	return nil
}
