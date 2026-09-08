// Package otelx builds the OpenTelemetry providers used by every service.
package otelx

import (
	"context"
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	"github.com/tunedev/go-resilient-commerce-lab/internal/version"
)

// Providers owns the telemetry stack for one service.
type Providers struct {
	// Registry is served by the /metrics endpoint.
	Registry *prometheus.Registry

	shutdown []func(context.Context) error
}

// Setup installs the global tracer provider, propagator and meter provider.
// Traces are exported OTLP over gRPC to otlpEndpoint. Metrics are exposed
// through Registry for Prometheus to scrape directly.
func Setup(ctx context.Context, serviceName, otlpEndpoint string) (*Providers, error) {
	res, err := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version.Value),
			attribute.String("deployment.environment.name", "lab"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("otelx: resource: %w", err)
	}

	traceExporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(otlpEndpoint),
		otlptracegrpc.WithInsecure(),
	)
	if err != nil {
		return nil, fmt.Errorf("otelx: trace exporter: %w", err)
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter),
		sdktrace.WithResource(res),
	)

	registry := prometheus.NewRegistry()
	metricReader, err := promexporter.New(promexporter.WithRegisterer(registry))
	if err != nil {
		// NewTracerProvider has already started its batch processor goroutine.
		// Stop it rather than leaking it behind a Setup that returns no handle
		// the caller could shut down.
		return nil, errors.Join(
			fmt.Errorf("otelx: metric exporter: %w", err),
			tracerProvider.Shutdown(ctx),
		)
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(metricReader),
		sdkmetric.WithResource(res),
	)

	// Globals are installed only once every provider is built, so a failed
	// Setup leaves the process with no half-installed telemetry.
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return &Providers{
		Registry: registry,
		shutdown: []func(context.Context) error{
			tracerProvider.Shutdown,
			meterProvider.Shutdown,
		},
	}, nil
}

// Shutdown flushes and stops every provider.
func (p *Providers) Shutdown(ctx context.Context) error {
	errs := make([]error, 0, len(p.shutdown))
	for _, fn := range p.shutdown {
		errs = append(errs, fn(ctx))
	}
	return errors.Join(errs...)
}
