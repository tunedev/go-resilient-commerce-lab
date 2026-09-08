package app

import (
	"context"

	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

// GetOrder reads a single order.
type GetOrder struct {
	store OrderStore
}

// NewGetOrder builds the use case.
func NewGetOrder(store OrderStore) *GetOrder {
	return &GetOrder{store: store}
}

// Execute returns the order, or domain.ErrOrderNotFound.
func (uc *GetOrder) Execute(ctx context.Context, id string) (domain.Order, error) {
	return uc.store.GetOrder(ctx, id)
}
