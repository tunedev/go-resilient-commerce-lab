// Package postgres implements the inventory service ports against Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	infrapg "github.com/tunedev/go-resilient-commerce-lab/internal/postgres" // aliased: this package is also named postgres
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

// pgUniqueViolation is the Postgres SQLSTATE for a unique constraint violation.
const pgUniqueViolation = "23505"

// idempotencyKeyConstraint is the auto-generated name of the UNIQUE
// constraint on inventory_reservations.idempotency_key.
const idempotencyKeyConstraint = "inventory_reservations_idempotency_key_key"

// Store is the InventoryStore implementation.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Reserve guards the stock decrement in the UPDATE's WHERE clause, so no
// read-then-write window exists between checking availability and taking it.
func (s *Store) Reserve(ctx context.Context, r domain.Reservation, events []outbox.Event) error {
	return infrapg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		const updateStock = `
UPDATE inventory_items
   SET available_quantity = available_quantity - $2,
       reserved_quantity  = reserved_quantity  + $2,
       version            = version + 1,
       updated_at         = now()
 WHERE sku = $1
   AND available_quantity >= $2`

		tag, err := tx.Exec(ctx, updateStock, r.SKU, r.Quantity)
		if err != nil {
			return fmt.Errorf("inventory: reserve stock %s: %w", r.SKU, err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrInsufficientStock
		}

		const insertReservation = `
INSERT INTO inventory_reservations (id, order_id, sku, quantity, status, expires_at, idempotency_key)
VALUES ($1, $2, $3, $4, $5, $6, $7)`

		_, err = tx.Exec(ctx, insertReservation,
			r.ID, r.OrderID, r.SKU, r.Quantity, r.Status, r.ExpiresAt, r.IdempotencyKey)
		if err != nil {
			if isUniqueViolation(err, idempotencyKeyConstraint) {
				return app.ErrDuplicateKey
			}
			return fmt.Errorf("inventory: insert reservation %s: %w", r.ID, err)
		}

		return outbox.Append(ctx, tx, events...)
	})
}

// ReservationByKey returns the reservation carrying key.
func (s *Store) ReservationByKey(ctx context.Context, key string) (domain.Reservation, error) {
	const query = `
SELECT id, order_id, sku, quantity, status, expires_at, idempotency_key, created_at, updated_at
  FROM inventory_reservations
 WHERE idempotency_key = $1`

	r, err := scanReservation(s.pool.QueryRow(ctx, query, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Reservation{}, domain.ErrReservationNotFound
	}
	if err != nil {
		return domain.Reservation{}, fmt.Errorf("inventory: reservation by key: %w", err)
	}
	return r, nil
}

// Commit moves the reservation's quantity out of reserved_quantity, guarding
// the status transition in the same WHERE clause as the domain state machine.
func (s *Store) Commit(ctx context.Context, id string, events []outbox.Event) (domain.Reservation, error) {
	var result domain.Reservation
	err := infrapg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := s.transitionReservation(ctx, tx, id, domain.StatusCommitted)
		if err != nil {
			return err
		}

		const updateItem = `
UPDATE inventory_items
   SET reserved_quantity = reserved_quantity - $2,
       version           = version + 1,
       updated_at        = now()
 WHERE sku = $1`

		if _, err := tx.Exec(ctx, updateItem, r.SKU, r.Quantity); err != nil {
			return fmt.Errorf("inventory: commit decrement reserved %s: %w", r.SKU, err)
		}

		if err := outbox.Append(ctx, tx, events...); err != nil {
			return err
		}
		result = r
		return nil
	})
	return result, err
}

// Release returns the reservation's quantity to available_quantity. A
// reservation not currently in the reserved state fails the same WHERE guard
// as Commit, so a committed reservation cannot be released.
func (s *Store) Release(ctx context.Context, id string, events []outbox.Event) (domain.Reservation, error) {
	var result domain.Reservation
	err := infrapg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := s.transitionReservation(ctx, tx, id, domain.StatusReleased)
		if err != nil {
			return err
		}

		const updateItem = `
UPDATE inventory_items
   SET available_quantity = available_quantity + $2,
       reserved_quantity  = reserved_quantity  - $2,
       version            = version + 1,
       updated_at         = now()
 WHERE sku = $1`

		if _, err := tx.Exec(ctx, updateItem, r.SKU, r.Quantity); err != nil {
			return fmt.Errorf("inventory: release return available %s: %w", r.SKU, err)
		}

		if err := outbox.Append(ctx, tx, events...); err != nil {
			return err
		}
		result = r
		return nil
	})
	return result, err
}

// transitionReservation moves the reservation to next, guarding the edge in
// the WHERE clause. No rows returned is ambiguous between the id not existing
// and the reservation being in some other status, so it is resolved by a
// follow-up read rather than assumed.
func (s *Store) transitionReservation(ctx context.Context, tx pgx.Tx, id string, next domain.Status) (domain.Reservation, error) {
	const update = `
UPDATE inventory_reservations
   SET status = $2, updated_at = now()
 WHERE id = $1 AND status = 'reserved'
RETURNING id, order_id, sku, quantity, status, expires_at, idempotency_key, created_at, updated_at`

	r, err := scanReservation(tx.QueryRow(ctx, update, id, next))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Reservation{}, classifyMissingTransition(ctx, tx, id, next)
	}
	if err != nil {
		return domain.Reservation{}, fmt.Errorf("inventory: transition reservation %s to %s: %w", id, next, err)
	}
	return r, nil
}

// classifyMissingTransition distinguishes a missing reservation from one that
// exists but cannot legally move to next.
func classifyMissingTransition(ctx context.Context, tx pgx.Tx, id string, next domain.Status) error {
	var status domain.Status
	err := tx.QueryRow(ctx, `SELECT status FROM inventory_reservations WHERE id = $1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrReservationNotFound
	}
	if err != nil {
		return fmt.Errorf("inventory: read reservation %s status: %w", id, err)
	}
	return fmt.Errorf("%w: %s -> %s", domain.ErrIllegalTransition, status, next)
}

// SetStock upserts sku's available quantity as an absolute value; it never
// accumulates across calls.
func (s *Store) SetStock(ctx context.Context, sku string, quantity int) (domain.Item, error) {
	const upsert = `
INSERT INTO inventory_items (sku, available_quantity)
VALUES ($1, $2)
ON CONFLICT (sku) DO UPDATE
   SET available_quantity = EXCLUDED.available_quantity,
       version            = inventory_items.version + 1,
       updated_at         = now()
RETURNING sku, available_quantity, reserved_quantity, version`

	var item domain.Item
	err := s.pool.QueryRow(ctx, upsert, sku, quantity).Scan(
		&item.SKU, &item.AvailableQuantity, &item.ReservedQuantity, &item.Version)
	if err != nil {
		return domain.Item{}, fmt.Errorf("inventory: set stock %s: %w", sku, err)
	}
	return item, nil
}

// AppendEvents writes events in their own transaction.
func (s *Store) AppendEvents(ctx context.Context, events []outbox.Event) error {
	return infrapg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		return outbox.Append(ctx, tx, events...)
	})
}

// ExpireDue claims reservations past their expiry with FOR UPDATE SKIP
// LOCKED, so concurrent sweepers take disjoint batches, then expires each one
// and returns its quantity to available stock in the same transaction.
func (s *Store) ExpireDue(ctx context.Context, limit int, buildEvent func(domain.Reservation) (outbox.Event, error)) (int, error) {
	expired := 0
	err := infrapg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		due, err := claimDueReservations(ctx, tx, limit)
		if err != nil {
			return err
		}

		for _, r := range due {
			ok, err := expireOne(ctx, tx, r, buildEvent)
			if err != nil {
				return err
			}
			if ok {
				expired++
			}
		}
		return nil
	})
	return expired, err
}

func claimDueReservations(ctx context.Context, tx pgx.Tx, limit int) ([]domain.Reservation, error) {
	const claim = `
SELECT id, order_id, sku, quantity, status, expires_at, idempotency_key
  FROM inventory_reservations
 WHERE status = 'reserved'
   AND expires_at <= now()
 ORDER BY expires_at
 FOR UPDATE SKIP LOCKED
 LIMIT $1`

	rows, err := tx.Query(ctx, claim, limit)
	if err != nil {
		return nil, fmt.Errorf("inventory: claim due reservations: %w", err)
	}
	defer rows.Close()

	var due []domain.Reservation
	for rows.Next() {
		var r domain.Reservation
		if err := rows.Scan(&r.ID, &r.OrderID, &r.SKU, &r.Quantity, &r.Status, &r.ExpiresAt, &r.IdempotencyKey); err != nil {
			return nil, fmt.Errorf("inventory: scan due reservation: %w", err)
		}
		due = append(due, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inventory: due reservation rows: %w", err)
	}
	return due, nil
}

// expireOne guards the expiry in the same WHERE clause every other transition
// in this file uses. Zero rows affected means a concurrent sweeper already
// expired r; that is not an error, but it must not credit stock twice, so it
// reports itself unexpired and stops. This WHERE guard is what makes double
// crediting impossible; FOR UPDATE SKIP LOCKED only decides whether a second
// sweeper blocks on this row or moves on to a different one.
func expireOne(ctx context.Context, tx pgx.Tx, r domain.Reservation, buildEvent func(domain.Reservation) (outbox.Event, error)) (bool, error) {
	const updateReservation = `
UPDATE inventory_reservations
   SET status = 'expired', updated_at = now()
 WHERE id = $1 AND status = 'reserved'`

	tag, err := tx.Exec(ctx, updateReservation, r.ID)
	if err != nil {
		return false, fmt.Errorf("inventory: expire reservation %s: %w", r.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	const updateItem = `
UPDATE inventory_items
   SET available_quantity = available_quantity + $2,
       reserved_quantity  = reserved_quantity  - $2,
       version            = version + 1,
       updated_at         = now()
 WHERE sku = $1`

	if _, err := tx.Exec(ctx, updateItem, r.SKU, r.Quantity); err != nil {
		return false, fmt.Errorf("inventory: expire return available %s: %w", r.SKU, err)
	}

	r.Status = domain.StatusExpired
	event, err := buildEvent(r)
	if err != nil {
		return false, err
	}
	if err := outbox.Append(ctx, tx, event); err != nil {
		return false, err
	}
	return true, nil
}

// row is satisfied by both pgxpool.Pool.QueryRow and pgx.Tx.QueryRow.
type row interface {
	Scan(dest ...any) error
}

func scanReservation(r row) (domain.Reservation, error) {
	var res domain.Reservation
	err := r.Scan(&res.ID, &res.OrderID, &res.SKU, &res.Quantity, &res.Status,
		&res.ExpiresAt, &res.IdempotencyKey, &res.CreatedAt, &res.UpdatedAt)
	return res, err
}

func isUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == constraint
}
