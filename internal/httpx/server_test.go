package httpx_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/logger"
)

func TestServerServesThenShutsDownOnContextCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	mux := http.NewServeMux()
	httpx.Route(mux, "GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	var buf bytes.Buffer
	srv := httpx.NewServer(httpx.Options{
		Addr:            addr,
		Handler:         mux,
		Logger:          logger.New(&buf, "order", slog.LevelInfo),
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	waitForHealthy(t, "http://"+addr+"/healthz")

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestServerLogsAfterRecoveringAPanic(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	mux := http.NewServeMux()
	httpx.Route(mux, "GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	httpx.Route(mux, "GET /panic", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	var buf bytes.Buffer
	srv := httpx.NewServer(httpx.Options{
		Addr:            addr,
		Handler:         mux,
		Logger:          logger.New(&buf, "order", slog.LevelInfo),
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	waitForHealthy(t, "http://"+addr+"/healthz")

	resp, err := http.Get("http://" + addr + "/panic") //nolint:noctx // short-lived probe in a test
	if err != nil {
		t.Fatalf("GET /panic: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if !strings.Contains(string(body), "internal_error") {
		t.Errorf("body = %q, want the internal_error envelope", body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	logged := buf.String()
	if !strings.Contains(logged, `"path":"/panic"`) || !strings.Contains(logged, `"status":500`) {
		t.Fatalf("log output = %q, want a request line recording status 500 for /panic", logged)
	}
}

func waitForHealthy(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // short-lived probe in a test
		if err == nil {
			status := resp.StatusCode
			if closeErr := resp.Body.Close(); closeErr != nil {
				t.Logf("close probe response body: %v", closeErr)
			}
			if status == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server at %s never became healthy", url)
}
