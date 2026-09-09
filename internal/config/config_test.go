package config_test

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/tunedev/go-resilient-commerce-lab/internal/config"
)

func TestLoadAppliesDefaults(t *testing.T) {
	t.Setenv("DATABASE_DSN", "postgres://localhost/orders")

	cfg, err := config.Load("order")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.ServiceName != "order" {
		t.Errorf("ServiceName = %q, want %q", cfg.ServiceName, "order")
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":8080")
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelInfo)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.ShutdownTimeout, 15*time.Second)
	}
	if cfg.OutboxPollInterval != time.Second {
		t.Errorf("OutboxPollInterval = %v, want %v", cfg.OutboxPollInterval, time.Second)
	}
	if cfg.OTLPEndpoint != "localhost:4317" {
		t.Errorf("OTLPEndpoint = %q, want %q", cfg.OTLPEndpoint, "localhost:4317")
	}
	if cfg.ReservationTTL != 15*time.Minute {
		t.Errorf("ReservationTTL = %v, want %v", cfg.ReservationTTL, 15*time.Minute)
	}
	if cfg.ReservationSweepInterval != 30*time.Second {
		t.Errorf("ReservationSweepInterval = %v, want %v", cfg.ReservationSweepInterval, 30*time.Second)
	}
}

func TestLoadFailsWithoutRequiredValue(t *testing.T) {
	t.Setenv("DATABASE_DSN", "")

	_, err := config.Load("order")
	if err == nil {
		t.Fatal("Load succeeded without DATABASE_DSN, want error")
	}
	if !strings.Contains(err.Error(), "DATABASE_DSN") {
		t.Errorf("error %q does not name the missing variable", err)
	}
}

func TestLoadReadsOverrides(t *testing.T) {
	t.Setenv("DATABASE_DSN", "postgres://localhost/orders")
	t.Setenv("HTTP_ADDR", ":9999")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("OUTBOX_POLL_INTERVAL", "250ms")
	t.Setenv("RESERVATION_TTL", "5m")
	t.Setenv("RESERVATION_SWEEP_INTERVAL", "10s")

	cfg, err := config.Load("order")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.HTTPAddr != ":9999" {
		t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":9999")
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, slog.LevelDebug)
	}
	if cfg.OutboxPollInterval != 250*time.Millisecond {
		t.Errorf("OutboxPollInterval = %v, want %v", cfg.OutboxPollInterval, 250*time.Millisecond)
	}
	if cfg.ReservationTTL != 5*time.Minute {
		t.Errorf("ReservationTTL = %v, want %v", cfg.ReservationTTL, 5*time.Minute)
	}
	if cfg.ReservationSweepInterval != 10*time.Second {
		t.Errorf("ReservationSweepInterval = %v, want %v", cfg.ReservationSweepInterval, 10*time.Second)
	}
}

func TestLoadRejectsMalformedDuration(t *testing.T) {
	t.Setenv("DATABASE_DSN", "postgres://localhost/orders")
	t.Setenv("OUTBOX_POLL_INTERVAL", "soon")

	if _, err := config.Load("order"); err == nil {
		t.Fatal("Load accepted a malformed duration, want error")
	}
}

func TestLoadRejectsUnknownLogLevel(t *testing.T) {
	t.Setenv("DATABASE_DSN", "postgres://localhost/orders")
	t.Setenv("LOG_LEVEL", "chatty")

	if _, err := config.Load("order"); err == nil {
		t.Fatal("Load accepted an unknown log level, want error")
	}
}
