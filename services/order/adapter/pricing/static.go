// Package pricing provides unit prices for the order service.
package pricing

import (
	"context"
	"fmt"

	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
)

// Static prices a fixed catalogue. Epic C replaces it if real pricing is needed.
type Static struct {
	prices map[string]int64
}

// NewStatic returns the lab catalogue. Prices are in minor currency units.
func NewStatic() *Static {
	return &Static{prices: map[string]int64{
		"playstation-5": 150000,
		"xbox-series-x": 140000,
		"switch-2":      95000,
	}}
}

// UnitPrice returns the price of one unit of sku.
func (s *Static) UnitPrice(_ context.Context, sku string) (int64, error) {
	price, ok := s.prices[sku]
	if !ok {
		return 0, fmt.Errorf("%w: %s", app.ErrUnknownSKU, sku)
	}
	return price, nil
}

// Currency returns the currency every price is quoted in.
func (s *Static) Currency() string {
	return "NGN"
}
