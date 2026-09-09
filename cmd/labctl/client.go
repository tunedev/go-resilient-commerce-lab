package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type response struct {
	Status int
	Body   []byte
	Header http.Header
}

// serviceClient holds the HTTP plumbing shared by the lab's service clients:
// issuing a request and reading its response, and polling /readyz.
type serviceClient struct {
	baseURL string
	http    *http.Client
}

func newServiceClient(baseURL string) serviceClient {
	return serviceClient{baseURL: baseURL, http: &http.Client{Timeout: 10 * time.Second}}
}

func (c *serviceClient) do(req *http.Request) (response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return response{}, fmt.Errorf("read response: %w", err)
	}
	return response{Status: resp.StatusCode, Body: body, Header: resp.Header}, nil
}

// waitReady polls /readyz until the service is up or the deadline passes.
func (c *serviceClient) waitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/readyz", nil)
		if err != nil {
			return err
		}
		if resp, err := c.do(req); err == nil && resp.Status == http.StatusOK {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s was not ready within %s", c.baseURL, timeout)
}

type orderClient struct {
	serviceClient
}

func newOrderClient(baseURL string) *orderClient {
	return &orderClient{newServiceClient(baseURL)}
}

func (c *orderClient) createOrder(ctx context.Context, idempotencyKey string, body any) (response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return response{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/orders", bytes.NewReader(payload))
	if err != nil {
		return response{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	return c.do(req)
}

func (c *orderClient) getOrder(ctx context.Context, id string) (response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/orders/"+id, nil)
	if err != nil {
		return response{}, fmt.Errorf("build request: %w", err)
	}
	return c.do(req)
}

type inventoryClient struct {
	serviceClient
}

func newInventoryClient(baseURL string) *inventoryClient {
	return &inventoryClient{newServiceClient(baseURL)}
}

func (c *inventoryClient) setStock(ctx context.Context, sku string, availableQuantity int) (response, error) {
	payload, err := json.Marshal(map[string]any{"available_quantity": availableQuantity})
	if err != nil {
		return response{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/inventory/items/"+sku, bytes.NewReader(payload))
	if err != nil {
		return response{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	return c.do(req)
}

func (c *inventoryClient) reserve(ctx context.Context, orderID, sku string, quantity int, idempotencyKey string) (response, error) {
	payload, err := json.Marshal(map[string]any{
		"order_id":        orderID,
		"sku":             sku,
		"quantity":        quantity,
		"idempotency_key": idempotencyKey,
	})
	if err != nil {
		return response{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/inventory/reservations", bytes.NewReader(payload))
	if err != nil {
		return response{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	return c.do(req)
}

// ping issues a single readiness check, which the server answers by acquiring
// a pooled database connection. Concurrent calls make the server construct
// physical connections it does not already hold idle.
func (c *inventoryClient) ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/readyz", nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	if resp.Status != http.StatusOK {
		return fmt.Errorf("readyz returned %d", resp.Status)
	}
	return nil
}
