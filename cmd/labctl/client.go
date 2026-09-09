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

type orderClient struct {
	baseURL string
	http    *http.Client
}

func newOrderClient(baseURL string) *orderClient {
	return &orderClient{baseURL: baseURL, http: &http.Client{Timeout: 10 * time.Second}}
}

type response struct {
	Status int
	Body   []byte
	Header http.Header
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

func (c *orderClient) do(req *http.Request) (response, error) {
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
func (c *orderClient) waitReady(ctx context.Context, timeout time.Duration) error {
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
