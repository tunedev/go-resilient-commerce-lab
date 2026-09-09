// Package config loads service configuration from the environment.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

// Config is the configuration shared by every service binary.
type Config struct {
	ServiceName        string
	HTTPAddr           string
	DatabaseDSN        string
	OTLPEndpoint       string
	LogLevel           slog.Level
	OutboxPollInterval time.Duration
	ShutdownTimeout    time.Duration
}

// Load reads configuration for the named service. It returns an error naming
// the first variable that is required but unset, or that holds a malformed value.
func Load(serviceName string) (Config, error) {
	dsn, ok := os.LookupEnv("DATABASE_DSN")
	if !ok || dsn == "" {
		return Config{}, fmt.Errorf("config: DATABASE_DSN is required")
	}

	level, err := parseLevel(env("LOG_LEVEL", "info"))
	if err != nil {
		return Config{}, err
	}

	pollInterval, err := parseDuration("OUTBOX_POLL_INTERVAL", time.Second)
	if err != nil {
		return Config{}, err
	}

	shutdownTimeout, err := parseDuration("SHUTDOWN_TIMEOUT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}

	return Config{
		ServiceName:        serviceName,
		HTTPAddr:           env("HTTP_ADDR", ":8080"),
		DatabaseDSN:        dsn,
		OTLPEndpoint:       env("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317"),
		LogLevel:           level,
		OutboxPollInterval: pollInterval,
		ShutdownTimeout:    shutdownTimeout,
	}, nil
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func parseDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %w", key, err)
	}
	return d, nil
}

func parseLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(raw) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: LOG_LEVEL: unknown level %q", raw)
	}
}
