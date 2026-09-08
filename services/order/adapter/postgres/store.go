// Package postgres implements the order service ports against Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	infrapg "github.com/tunedev/go-resilient-commerce-lab/internal/postgres" // aliased: this package is also named postgres
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

// Store is the OrderStore implementation.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateOrder writes the order, its items, the outbox events and the
// idempotency completion in one transaction.
func (s *Store) CreateOrder(ctx context.Context, order domain.Order, events []outbox.Event, completion *idempotency.Completion) error {
	return infrapg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		const insertOrder = `
INSERT INTO orders (id, customer_id, status, total_amount, currency, payment_method_id)
VALUES ($1, $2, $3, $4, $5, $6)`

		_, err := tx.Exec(ctx, insertOrder, order.ID, order.CustomerID, order.Status,
			order.TotalAmount, order.Currency, order.PaymentMethodID)
		if err != nil {
			return fmt.Errorf("order: insert order: %w", err)
		}

		const insertItem = `
INSERT INTO order_items (order_id, sku, quantity, unit_price) VALUES ($1, $2, $3, $4)`

		for _, item := range order.Items {
			if _, err := tx.Exec(ctx, insertItem, order.ID, item.SKU, item.Quantity, item.UnitPrice); err != nil {
				return fmt.Errorf("order: insert item %s: %w", item.SKU, err)
			}
		}

		if err := outbox.Append(ctx, tx, events...); err != nil {
			return err
		}

		if completion != nil {
			if err := idempotency.Complete(ctx, tx, *completion); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetOrder returns the order with its items.
func (s *Store) GetOrder(ctx context.Context, id string) (domain.Order, error) {
	const selectOrder = `
SELECT id, customer_id, status, total_amount, currency, payment_method_id, created_at, updated_at
  FROM orders WHERE id = $1`

	var o domain.Order
	err := s.pool.QueryRow(ctx, selectOrder, id).Scan(
		&o.ID, &o.CustomerID, &o.Status, &o.TotalAmount,
		&o.Currency, &o.PaymentMethodID, &o.CreatedAt, &o.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, domain.ErrOrderNotFound
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("order: select order %s: %w", id, err)
	}

	const selectItems = `SELECT sku, quantity, unit_price FROM order_items WHERE order_id = $1 ORDER BY sku`

	rows, err := s.pool.Query(ctx, selectItems, id)
	if err != nil {
		return domain.Order{}, fmt.Errorf("order: select items %s: %w", id, err)
	}
	defer rows.Close()

	for rows.Next() {
		var item domain.Item
		if err := rows.Scan(&item.SKU, &item.Quantity, &item.UnitPrice); err != nil {
			return domain.Order{}, fmt.Errorf("order: scan item: %w", err)
		}
		o.Items = append(o.Items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Order{}, fmt.Errorf("order: item rows: %w", err)
	}
	return o, nil
}
