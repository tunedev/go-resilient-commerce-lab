package otelx_test

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel"

	"github.com/tunedev/go-resilient-commerce-lab/internal/otelx"
)

func TestSetupSucceedsWithoutACollector(t *testing.T) {
	providers, err := otelx.Setup(context.Background(), "order", "127.0.0.1:4317")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() {
		if err := providers.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	if providers.Registry == nil {
		t.Fatal("Registry is nil")
	}
}

func TestSetupInstallsATracerThatRecords(t *testing.T) {
	providers, err := otelx.Setup(context.Background(), "order", "127.0.0.1:4317")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() {
		// Bounds the flush wait; the export is expected to fail against an
		// absent collector and is not asserted here.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_ = providers.Shutdown(shutdownCtx)
	})

	_, span := otel.Tracer("test").Start(context.Background(), "unit")
	defer span.End()

	if !span.SpanContext().IsValid() {
		t.Fatal("span context is not valid; no tracer provider installed")
	}
}

func TestSetupInstallsTraceContextPropagator(t *testing.T) {
	providers, err := otelx.Setup(context.Background(), "order", "127.0.0.1:4317")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = providers.Shutdown(context.Background()) })

	fields := otel.GetTextMapPropagator().Fields()
	var hasTraceparent bool
	for _, f := range fields {
		if f == "traceparent" {
			hasTraceparent = true
		}
	}
	if !hasTraceparent {
		t.Fatalf("propagator fields %v do not include traceparent", fields)
	}
}
