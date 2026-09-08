package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"github.com/tunedev/go-resilient-commerce-lab/internal/logger"
)

func spanContext(t *testing.T) (context.Context, trace.SpanContext) {
	t.Helper()
	traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	if err != nil {
		t.Fatalf("TraceIDFromHex: %v", err)
	}
	spanID, err := trace.SpanIDFromHex("0102030405060708")
	if err != nil {
		t.Fatalf("SpanIDFromHex: %v", err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
	})
	return trace.ContextWithSpanContext(context.Background(), sc), sc
}

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	return got
}

func TestRecordCarriesServiceName(t *testing.T) {
	var buf bytes.Buffer
	logger.New(&buf, "order", slog.LevelInfo).Info("hello")

	if got := decode(t, &buf)["service"]; got != "order" {
		t.Errorf("service = %v, want %q", got, "order")
	}
}

func TestRecordInSpanCarriesTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	ctx, sc := spanContext(t)

	logger.New(&buf, "order", slog.LevelInfo).InfoContext(ctx, "hello")

	got := decode(t, &buf)
	if got["trace_id"] != sc.TraceID().String() {
		t.Errorf("trace_id = %v, want %v", got["trace_id"], sc.TraceID())
	}
	if got["span_id"] != sc.SpanID().String() {
		t.Errorf("span_id = %v, want %v", got["span_id"], sc.SpanID())
	}
}

func TestRecordWithoutSpanOmitsTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	logger.New(&buf, "order", slog.LevelInfo).Info("hello")

	got := decode(t, &buf)
	if _, ok := got["trace_id"]; ok {
		t.Error("trace_id present without a span")
	}
	if _, ok := got["span_id"]; ok {
		t.Error("span_id present without a span")
	}
}

func TestWithAttrsPreservesTraceInjection(t *testing.T) {
	var buf bytes.Buffer
	ctx, sc := spanContext(t)

	logger.New(&buf, "order", slog.LevelInfo).
		With(slog.String("order_id", "ord_1")).
		InfoContext(ctx, "hello")

	got := decode(t, &buf)
	if got["order_id"] != "ord_1" {
		t.Errorf("order_id = %v, want %q", got["order_id"], "ord_1")
	}
	if got["trace_id"] != sc.TraceID().String() {
		t.Error("With() dropped trace injection")
	}
}

func TestLevelIsRespected(t *testing.T) {
	var buf bytes.Buffer
	logger.New(&buf, "order", slog.LevelWarn).Info("suppressed")

	if buf.Len() != 0 {
		t.Errorf("info record emitted at warn level: %s", buf.String())
	}
}
