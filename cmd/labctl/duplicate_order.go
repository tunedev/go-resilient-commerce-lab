package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func init() {
	register(scenario{
		name:        "duplicate-order",
		description: "the same Idempotency-Key twice yields one order, not two",
		service:     "order",
		run:         runDuplicateOrder,
	})
}

type createOrderResult struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
}

func runDuplicateOrder(ctx context.Context, env environment) error {
	client := newOrderClient(env.orderBaseURL)
	if err := client.waitReady(ctx, 60*time.Second); err != nil {
		return err
	}

	key := fmt.Sprintf("duplicate-order-%d", time.Now().UnixNano())
	body := map[string]any{
		"customer_id":       "cust_lab",
		"payment_method_id": "pm_ok",
		"items":             []map[string]any{{"sku": "playstation-5", "quantity": 1}},
	}

	first, err := client.createOrder(ctx, key, body)
	if err != nil {
		return err
	}
	if first.Status != http.StatusAccepted {
		return fmt.Errorf("first POST /orders returned %d: %s", first.Status, first.Body)
	}

	second, err := client.createOrder(ctx, key, body)
	if err != nil {
		return err
	}
	if second.Status != first.Status {
		return fmt.Errorf("replay returned %d, want %d", second.Status, first.Status)
	}
	if string(second.Body) != string(first.Body) {
		return fmt.Errorf("replay body %s differs from the original %s", second.Body, first.Body)
	}
	if second.Header.Get("Idempotent-Replay") != "true" {
		return fmt.Errorf("replay was not marked with Idempotent-Replay")
	}

	conflicting := map[string]any{
		"customer_id":       "cust_someone_else",
		"payment_method_id": "pm_ok",
		"items":             []map[string]any{{"sku": "playstation-5", "quantity": 1}},
	}
	conflict, err := client.createOrder(ctx, key, conflicting)
	if err != nil {
		return err
	}
	if conflict.Status != http.StatusConflict {
		return fmt.Errorf("reusing the key with a different body returned %d, want 409", conflict.Status)
	}

	var created createOrderResult
	if err := json.Unmarshal(first.Body, &created); err != nil {
		return fmt.Errorf("parse create response: %w", err)
	}

	fetched, err := client.getOrder(ctx, created.OrderID)
	if err != nil {
		return err
	}
	if fetched.Status != http.StatusOK {
		return fmt.Errorf("GET /orders/%s returned %d", created.OrderID, fetched.Status)
	}

	fmt.Printf("one order created: %s\n", created.OrderID)
	return nil
}
