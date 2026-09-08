package pricing_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/pricing"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
)

func TestUnitPriceKnownSKU(t *testing.T) {
	price, err := pricing.NewStatic().UnitPrice(context.Background(), "playstation-5")
	if err != nil {
		t.Fatalf("UnitPrice: %v", err)
	}
	if price <= 0 {
		t.Errorf("price = %d, want positive", price)
	}
}

func TestUnitPriceUnknownSKU(t *testing.T) {
	_, err := pricing.NewStatic().UnitPrice(context.Background(), "no-such-sku")
	if !errors.Is(err, app.ErrUnknownSKU) {
		t.Fatalf("err = %v, want ErrUnknownSKU", err)
	}
}

func TestCurrency(t *testing.T) {
	if got := pricing.NewStatic().Currency(); got != "NGN" {
		t.Errorf("Currency = %q, want NGN", got)
	}
}
