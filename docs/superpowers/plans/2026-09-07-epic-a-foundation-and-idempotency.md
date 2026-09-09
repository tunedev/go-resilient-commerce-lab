# Epic A — Foundation and Idempotency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a running order-service on its own Postgres database with idempotent
`POST /orders`, traced and logged and CI-gated from the first commit, plus the
one-command demo and the two blog posts that prove it.

**Architecture:** Single Go module. `internal/` holds shared infrastructure with no
business rules; `services/order/{domain,app,adapter}` holds the service, with port
interfaces declared in `app/` and implemented in `adapter/`. Every state change and
its outbox event commit in one Postgres transaction. OpenTelemetry, structured
logging and GitHub Actions exist before there is anything to observe or break.

**Tech Stack:** Go 1.27, `net/http` stdlib routing, pgx v5, goose v3, OpenTelemetry
Go SDK, Prometheus client, testcontainers-go, Docker Compose, Hugo.

**Spec:** `docs/superpowers/specs/2026-09-07-go-resilient-commerce-lab-design.md`

**Jira:** Epic A is PL-7. Stories PL-16 … PL-35. Each task below names the stories it closes.

## Global Constraints

- Module path is exactly `github.com/tunedev/go-resilient-commerce-lab`.
- Go directive in `go.mod` is `go 1.27.0`. Do not lower it.
- Pinned dependency versions, exact:
  - `github.com/jackc/pgx/v5 v5.10.0`
  - `github.com/pressly/goose/v3 v3.28.0`
  - `go.opentelemetry.io/otel v1.46.0`
  - `go.opentelemetry.io/otel/trace v1.46.0` (separate module from `otel`)
  - `go.opentelemetry.io/otel/metric v1.46.0` (separate module from `otel`)
  - `go.opentelemetry.io/otel/sdk v1.46.0`
  - `go.opentelemetry.io/otel/sdk/metric v1.46.0` (separate module from `otel/sdk`)
  - `go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.71.0`
  - `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.46.0`
  - `go.opentelemetry.io/otel/exporters/prometheus v0.68.0`
  - `github.com/prometheus/client_golang v1.24.1`
  - `github.com/testcontainers/testcontainers-go v0.44.0`
  - `github.com/testcontainers/testcontainers-go/modules/postgres v0.44.0`
  - `github.com/google/uuid v1.6.0`
- Pinned container images, exact:
  - `postgres:18.6`
  - `otel/opentelemetry-collector-contrib:0.160.0`
  - `jaegertracing/jaeger:2.20.0`
  - `prom/prometheus:v3.14.0`
  - `grafana/grafana:13.2.1`
- Tooling: `golangci-lint` v2.13.2 (v2 config schema — `version: "2"`, formatters in
  their own `formatters:` block), Hugo v0.165.0 extended.
- **No emojis** anywhere: not in code, log messages, print statements, commit
  messages, or documentation.
- Comments and doc comments describe current behaviour only. No history, no
  rationale paragraphs, no "this used to", no commit references, no counts that
  go stale.
- `internal/` must never import from `services/`. A package under `internal/` that
  knows what an order is belongs in `services/`.
- `services/order/domain` must not import a database driver, `net/http`, or
  anything from `adapter/`.
- Every exported function that performs I/O takes `ctx context.Context` first.
- Tests use the standard library `testing` package only. No assertion library:
  every assertion in this plan is a plain `if` with `t.Errorf`. `testify` may
  appear in `go.mod` as an indirect dependency of testcontainers; it is never
  imported by this code.
- Errors are wrapped with `fmt.Errorf("...: %w", err)`. Never discard an error to
  satisfy the linter.
- **Every commit must stand alone.** A task that runs `go get` stages `go.mod`
  and `go.sum` in the same commit as the code that imports them, and runs
  `go mod tidy` before committing so the module graph matches the imports.
- **Verify the commit, not the working tree.** A green `go build` in a dirty
  working tree proves nothing about what was committed. Before reporting, check
  the commit builds in isolation:

  ```bash
  T=$(mktemp -d) && git archive HEAD | tar -x -C "$T" && (cd "$T" && go build ./... && go test ./...) ; rm -rf "$T"
  ```

## Deviation from the spec, recorded here

Spec §7 says "the collector fans out to Jaeger and Prometheus". This plan splits
the two signals:

- **Traces** go OTLP/gRPC to the collector, which exports to Jaeger.
- **Metrics** are served in-process by each service on `/metrics` using the OTel
  Prometheus exporter, and Prometheus scrapes services directly.

Reason: story PL-28 requires a per-service `/metrics` endpoint, which a collector
funnel cannot provide. Direct scrape is also the more common production shape.
Update spec §7 when this task lands.

## File structure

```
go.mod  go.sum  Makefile  .golangci.yml  .gitignore  LICENSE  README.md
.github/
  workflows/ci.yml            lint, test, integration, build
  workflows/pages.yml         hugo build and deploy, main only
  PULL_REQUEST_TEMPLATE.md
cmd/
  order/main.go               composition root: config, otel, logger, db, http, workers
  labctl/main.go              scenario and chaos driver
internal/
  config/config.go            env loading, typed accessors, fail fast
  logger/logger.go            slog JSON handler injecting trace_id and span_id
  otelx/otelx.go              tracer and meter providers, OTLP exporter, Shutdown
  httpx/server.go             server construction and graceful shutdown
  httpx/errors.go             error envelope and domain-error to status mapping
  httpx/middleware.go         recovery, request id, otelhttp, request logging
  postgres/pool.go            pgxpool construction
  postgres/migrate.go         goose provider with Postgres session locker
  postgres/tx.go              WithTx helper
  outbox/store.go             Append inside a caller's transaction
  outbox/publisher.go         SKIP LOCKED claim loop
  outbox/sink.go              Sink interface and LogSink
  idempotency/store.go        claim, lookup, complete, release
  idempotency/hash.go         canonical request hashing
  idempotency/middleware.go   claim / replay / conflict / in-progress
services/order/
  domain/order.go             Status, Order, Item, transition table
  domain/errors.go            sentinel domain errors
  app/create_order.go         CreateOrder use case
  app/get_order.go            GetOrder use case
  app/ports.go                OrderRepository and dependencies, declared consumer-side
  adapter/http/handler.go     decode, validate, delegate, encode
  adapter/http/routes.go      route registration
  adapter/postgres/repo.go    OrderRepository implementation
  migrations/*.sql            goose SQL migrations, embedded
deploy/
  Dockerfile                  multi-stage, ARG SERVICE
  docker-compose.yml
  postgres/init.sql           creates the four logical databases
  otel-collector.yaml
  prometheus.yml
  grafana/provisioning/...
docs/
  hugo.toml
  content/posts/*.md
  content/architecture/*.md
  static/diagrams/system-topology.html
```

---

### Task 1: Repo scaffolding and the CI gate

**Closes:** PL-16, and the `lint` and `test` jobs of PL-31.

**Files:**
- Create: `go.mod`, `Makefile`, `.golangci.yml`, `.gitignore`, `LICENSE`
- Create: `.github/workflows/ci.yml`
- Create: `internal/version/version.go`, `internal/version/version_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `version.Value` (string constant, the value `"0.1.0"`), used by
  `otelx` as the `service.version` resource attribute.

- [ ] **Step 1: Initialise the module**

```bash
go mod init github.com/tunedev/go-resilient-commerce-lab
```

Then edit `go.mod` so the directive line reads exactly `go 1.27.0`.

- [ ] **Step 2: Write `.gitignore`**

```gitignore
/bin/
*.test
*.out
.env
.env.*
docs/public/
docs/resources/
docs/.hugo_build.lock
```

- [ ] **Step 3: Write `.golangci.yml` using the v2 schema**

v1 configuration is rejected by golangci-lint v2. `errcheck`, `govet`,
`ineffassign`, `staticcheck` and `unused` are enabled by default in v2, so they
are not listed. Formatters live in their own top-level block.

```yaml
version: "2"

linters:
  enable:
    - bodyclose
    - errorlint
    - revive
    - rowserrcheck
    - sqlclosecheck

formatters:
  enable:
    - gofmt
    - goimports
  settings:
    goimports:
      local-prefixes:
        - github.com/tunedev/go-resilient-commerce-lab
```

- [ ] **Step 4: Write the `Makefile`**

Recipe lines must be indented with real tab characters, not spaces.

There is deliberately no `migrate` target. Migrations run at service startup
under an advisory lock (Task 7), so a separate target would either duplicate
that or imply a mode the binary does not have.

```make
MODULE := github.com/tunedev/go-resilient-commerce-lab
COMPOSE := docker compose -f deploy/docker-compose.yml

.PHONY: fmt lint vet test test-race integration run docker-up docker-down scenario site

fmt:
	golangci-lint fmt ./...

lint:
	golangci-lint config verify
	golangci-lint run ./...

vet:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

integration:
	go test -race -tags=integration ./...

run:
	go run ./cmd/order

docker-up:
	$(COMPOSE) up -d --build

docker-down:
	$(COMPOSE) down -v

scenario:
	go run ./cmd/labctl scenario $(NAME)

site:
	cd docs && hugo server -D
```

- [ ] **Step 5: Write the failing test**

`internal/version/version_test.go`:

```go
package version_test

import (
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/version"
)

func TestValueIsSet(t *testing.T) {
	if version.Value == "" {
		t.Fatal("version.Value must not be empty")
	}
}
```

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./internal/version/...`
Expected: FAIL — `no required module provides package .../internal/version`

- [ ] **Step 7: Write the minimal implementation**

`internal/version/version.go`:

```go
// Package version carries the build version reported to telemetry.
package version

// Value is the version reported as the service.version resource attribute.
const Value = "0.1.0"
```

- [ ] **Step 8: Run the test to verify it passes**

Run: `go test ./internal/version/...`
Expected: PASS

- [ ] **Step 9: Write `.github/workflows/ci.yml`**

```yaml
name: ci

on:
  push:
    branches: [main]
  pull_request:

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: "1.27"
          cache: true
      - uses: golangci/golangci-lint-action@v9
        with:
          version: v2.13.2

  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: "1.27"
          cache: true
      - run: go test -race ./...
```

- [ ] **Step 10: Add the MIT LICENSE**

Standard MIT text, copyright holder `Babatunde Sanusi`, year `2026`.

- [ ] **Step 11: Verify the whole gate locally**

Run: `make lint && make test-race`
Expected: `golangci-lint config verify` reports the configuration is valid, the
run reports no issues, and tests pass. If `config verify` rejects the
`goimports.local-prefixes` key, drop the `settings:` block and keep the two
formatters — do not silently downgrade to the v1 schema.

- [ ] **Step 12: Commit**

```bash
git add go.mod Makefile .golangci.yml .gitignore LICENSE .github internal/version
git commit -m "chore: scaffold single Go module with lint and test gate

Single module rather than one per service: replace directives and inter-service
version churn buy nothing in a lab. golangci-lint v2 schema, with formatters in
their own block."
```

---

### Task 2: internal/config

**Closes:** PL-17.

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `type Config struct { ServiceName string; HTTPAddr string; DatabaseDSN string; OTLPEndpoint string; LogLevel slog.Level; OutboxPollInterval time.Duration; ShutdownTimeout time.Duration }`
  - `func Load(serviceName string) (Config, error)`

- [ ] **Step 1: Write the failing tests**

`internal/config/config_test.go`:

```go
package config_test

import (
	"log/slog"
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
}

func TestLoadRejectsUnknownLogLevel(t *testing.T) {
	t.Setenv("DATABASE_DSN", "postgres://localhost/orders")
	t.Setenv("LOG_LEVEL", "chatty")

	if _, err := config.Load("order"); err == nil {
		t.Fatal("Load accepted an unknown log level, want error")
	}
}

func TestLoadFailsWithoutRequiredValue(t *testing.T) {
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
}

func TestLoadRejectsMalformedDuration(t *testing.T) {
	t.Setenv("DATABASE_DSN", "postgres://localhost/orders")
	t.Setenv("OUTBOX_POLL_INTERVAL", "soon")

	if _, err := config.Load("order"); err == nil {
		t.Fatal("Load accepted a malformed duration, want error")
	}
}
```

Add `"strings"` to the import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config/...`
Expected: FAIL — package `internal/config` does not exist.

- [ ] **Step 3: Write the implementation**

`internal/config/config.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config/... -v`
Expected: PASS, five tests. All five documented defaults are asserted; leaving
`OutboxPollInterval` unasserted would let a regression zero it, and the outbox
publisher in Task 9 ticks on that value.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat(config): load service configuration from the environment

Fails at startup naming the missing variable rather than surfacing a nil DSN
as a connection error later."
```

---

### Task 3: internal/logger — slog JSON with trace correlation

**Closes:** PL-18.

**Files:**
- Create: `internal/logger/logger.go`, `internal/logger/logger_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func New(w io.Writer, serviceName string, level slog.Level) *slog.Logger`.
  `New` takes a writer so tests can assert on the emitted JSON; production callers
  pass `os.Stdout`.

The wrapper must override `WithAttrs` and `WithGroup`. A handler that only
embeds `slog.Handler` and overrides `Handle` loses the wrapper the first time a
caller writes `logger.With(...)`, because the embedded method returns the inner
handler. That is the bug this task exists to avoid.

- [ ] **Step 1: Add the dependency**

Both the test and the implementation import `go.opentelemetry.io/otel/trace`,
and nothing else. That path is its own Go module, separate from
`go.opentelemetry.io/otel`, so fetch it by name — and fetch only it, or the
module graph will carry requirements no code imports:

```bash
go get go.opentelemetry.io/otel/trace@v1.46.0
go mod tidy
```

- [ ] **Step 3: Write the failing tests**

`internal/logger/logger_test.go`:

```go
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
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/logger/...`
Expected: FAIL — package does not exist.

- [ ] **Step 4: Write the implementation**

`internal/logger/logger.go`:

```go
// Package logger builds the structured logger used by every service.
package logger

import (
	"context"
	"io"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

type traceHandler struct {
	inner slog.Handler
}

func (h traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{inner: h.inner.WithGroup(name)}
}

// New returns a JSON logger that stamps every record with the service name and,
// when the context carries a valid span, its trace and span ids.
func New(w io.Writer, serviceName string, level slog.Level) *slog.Logger {
	base := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(traceHandler{inner: base}).With(slog.String("service", serviceName))
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/logger/... -v`
Expected: PASS, five tests. `TestWithAttrsPreservesTraceInjection` is the one
that fails if the handler embeds instead of wrapping.

- [ ] **Step 6: Commit**

```bash
git add internal/logger go.mod go.sum
git commit -m "feat(logger): slog JSON handler injecting trace and span ids

The handler wraps rather than embeds slog.Handler, so WithAttrs and WithGroup
return the wrapper and logger.With does not silently drop trace injection."
```

---

### Task 4: internal/otelx — providers, OTLP exporter, shutdown

**Closes:** PL-19.

**Files:**
- Create: `internal/otelx/otelx.go`, `internal/otelx/otelx_test.go`

**Interfaces:**
- Consumes: `version.Value` from Task 1.
- Produces:
  - `type Providers struct { Registry *prometheus.Registry; ... }`
  - `func Setup(ctx context.Context, serviceName, otlpEndpoint string) (*Providers, error)`
  - `func (p *Providers) Shutdown(ctx context.Context) error`

`Providers.Registry` is the Prometheus registry the `/metrics` handler serves in
Task 12. Metrics are scraped from the service directly; only traces go through
the collector. See the deviation note above.

- [ ] **Step 1: Add the dependencies**

`go.opentelemetry.io/otel/trace` arrived in Task 3; the core
`go.opentelemetry.io/otel` module did not, because nothing imported it until
now. Note also that `otel/sdk` and `otel/sdk/metric` are two separate modules:
fetching the first does not bring the second, and this file imports both.

```bash
go get go.opentelemetry.io/otel@v1.46.0
go get go.opentelemetry.io/otel/sdk@v1.46.0
go get go.opentelemetry.io/otel/sdk/metric@v1.46.0
go get go.opentelemetry.io/otel/metric@v1.46.0
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc@v1.46.0
go get go.opentelemetry.io/otel/exporters/prometheus@v0.68.0
go get github.com/prometheus/client_golang@v1.24.1
```

- [ ] **Step 2: Write the failing tests**

`internal/otelx/otelx_test.go`:

```go
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
		// Bounds the flush wait. This is the only test that records a span, so
		// the only one whose Shutdown has anything buffered; the export is
		// expected to fail against an absent collector and is not asserted here.
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
```

`otlptracegrpc` does not dial on construction, so `Setup` succeeds with no
collector running. That is what makes these tests hermetic.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/otelx/...`
Expected: FAIL — package does not exist.

- [ ] **Step 4: Write the implementation**

`internal/otelx/otelx.go`:

```go
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
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/otelx/... -v`
Expected: PASS, three tests, in well under a second in total.

If the compiler reports that `go.opentelemetry.io/otel/semconv/v1.43.0` does not
exist, list the available versions with
`go list -m -versions go.opentelemetry.io/otel` and use the highest `semconv/vX`
directory present in the module at v1.46.0. Do not fall back to hand-written
`attribute.String("service.name", ...)`.

- [ ] **Step 6: Commit**

```bash
git add internal/otelx go.mod go.sum
git commit -m "feat(otelx): tracer and meter providers with OTLP trace export

Traces go OTLP to the collector; metrics are exposed on a per-service registry
for Prometheus to scrape directly, because a collector funnel cannot give each
service its own /metrics endpoint."
```

---

### Task 5: internal/httpx and the first runnable binary

**Closes:** PL-20, and the `/healthz` part of PL-28.

**Files:**
- Create: `internal/httpx/errors.go`, `internal/httpx/middleware.go`,
  `internal/httpx/server.go`, `internal/httpx/routes.go`
- Create: `internal/httpx/errors_test.go`, `internal/httpx/middleware_test.go`,
  `internal/httpx/server_test.go`
- Create: `cmd/order/main.go`

**Interfaces:**
- Consumes: `config.Config`, `logger.New`, `otelx.Setup`.
- Produces:
  - `func WriteJSON(ctx context.Context, w http.ResponseWriter, status int, v any)`
  - `func WriteError(ctx context.Context, w http.ResponseWriter, status int, code, message string, details any)`
  - `type ErrorResponse struct { Error ErrorBody }` with
    `type ErrorBody struct { Code, Message string; Details any }`
  - `func RequestID(next http.Handler) http.Handler`
  - `func RequestIDFromContext(ctx context.Context) string`
  - `func Recovery(l *slog.Logger) func(http.Handler) http.Handler`
  - `func RequestLogging(l *slog.Logger) func(http.Handler) http.Handler`
  - `func Route(mux *http.ServeMux, pattern string, h http.Handler)`
  - `type Options struct { Addr string; Handler http.Handler; Logger *slog.Logger; ShutdownTimeout time.Duration }`
  - `func NewServer(o Options) *Server` and `func (s *Server) Run(ctx context.Context) error`

`Route` exists so the server span is named after the route pattern rather than
the concrete path. Naming spans `GET /orders/ord_8f21...` produces one span name
per order, which makes latency aggregation useless. `r.Pattern` is only
populated after `ServeMux` has routed, so the rename has to happen inside the
handler, not in an outer middleware.

- [ ] **Step 1: Add the dependencies**

```bash
go get go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp@v0.71.0
go get github.com/google/uuid@v1.6.0
```

- [ ] **Step 2: Write the failing tests for the error envelope**

`internal/httpx/errors_test.go`:

```go
package httpx_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
)

func TestWriteErrorShape(t *testing.T) {
	rec := httptest.NewRecorder()

	httpx.WriteError(context.Background(), rec, http.StatusConflict,
		"idempotency_key_reused", "key reused with a different payload", nil)

	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got httpx.ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	if got.Error.Code != "idempotency_key_reused" {
		t.Errorf("code = %q", got.Error.Code)
	}
	if got.Error.Message == "" {
		t.Error("message is empty")
	}
}

func TestWriteErrorOmitsEmptyDetails(t *testing.T) {
	rec := httptest.NewRecorder()

	httpx.WriteError(context.Background(), rec, http.StatusBadRequest, "invalid_request", "bad", nil)

	var raw map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := raw["error"]["details"]; ok {
		t.Error("details present when nil")
	}
}
```

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/httpx/...`
Expected: FAIL — package does not exist.

- [ ] **Step 4: Write `internal/httpx/errors.go`**

```go
// Package httpx holds the HTTP server shell shared by every service.
package httpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
)

// ErrorBody is the body of an error response.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// ErrorResponse is the single error envelope every service returns.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// WriteJSON writes v as JSON with the given status. A nil v writes no body.
// It marshals rather than streaming through an Encoder, which appends a
// trailing newline: a stored idempotency response is replayed as raw bytes, so
// both paths must produce the same bytes for the same value.
func WriteJSON(ctx context.Context, w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")

	if v == nil {
		w.WriteHeader(status)
		return
	}

	body, err := json.Marshal(v)
	if err != nil {
		slog.ErrorContext(ctx, "marshal json response", slog.Any("error", err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		slog.ErrorContext(ctx, "write json response", slog.Any("error", err))
	}
}

// WriteError writes the error envelope.
func WriteError(ctx context.Context, w http.ResponseWriter, status int, code, message string, details any) {
	WriteJSON(ctx, w, status, ErrorResponse{
		Error: ErrorBody{Code: code, Message: message, Details: details},
	})
}
```

- [ ] **Step 5: Run to verify the envelope tests pass**

Run: `go test ./internal/httpx/... -run TestWriteError -v`
Expected: PASS, two tests.

- [ ] **Step 6: Write the failing middleware tests**

`internal/httpx/middleware_test.go`:

```go
package httpx_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/logger"
)

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	var seen string
	h := httpx.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestIDFromContext(r.Context())
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if seen == "" {
		t.Fatal("no request id in context")
	}
	if got := rec.Header().Get("X-Request-Id"); got != seen {
		t.Errorf("X-Request-Id header = %q, want %q", got, seen)
	}
}

func TestRequestIDHonoursInboundHeader(t *testing.T) {
	var seen string
	h := httpx.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestIDFromContext(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "req-abc")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "req-abc" {
		t.Errorf("request id = %q, want %q", seen, "req-abc")
	}
}

func TestRecoveryConvertsPanicToInternalError(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, "order", slog.LevelError)

	h := httpx.Recovery(l)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal_error") {
		t.Errorf("body = %q, want the internal_error envelope", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Error("panic value was not logged")
	}
}

func TestRequestLoggingRecordsStatus(t *testing.T) {
	var buf bytes.Buffer
	l := logger.New(&buf, "order", slog.LevelInfo)

	h := httpx.RequestLogging(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/orders", nil))

	out := buf.String()
	if !strings.Contains(out, `"status":418`) {
		t.Errorf("log %q does not record the status", out)
	}
	if !strings.Contains(out, `"method":"GET"`) {
		t.Errorf("log %q does not record the method", out)
	}
}
```

- [ ] **Step 7: Run to verify failure**

Run: `go test ./internal/httpx/... -run 'TestRequest|TestRecovery' -v`
Expected: FAIL — undefined identifiers.

- [ ] **Step 8: Write `internal/httpx/middleware.go`**

```go
package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type contextKey int

const requestIDKey contextKey = iota

const requestIDHeader = "X-Request-Id"

// RequestID reads an inbound request id or generates one, puts it in the
// context, and echoes it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set(requestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

// RequestIDFromContext returns the request id, or the empty string.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusRecorder) WriteHeader(status int) {
	if s.written {
		return
	}
	s.status = status
	s.written = true
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.written {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// Recovery turns a panic into a 500 error envelope and logs the panic value.
func Recovery(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					l.ErrorContext(r.Context(), "panic recovered",
						slog.Any("panic", v),
						slog.String("path", r.URL.Path),
					)
					WriteError(r.Context(), w, http.StatusInternalServerError,
						"internal_error", "internal error", nil)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogging logs one record per request with method, path, status and duration.
func RequestLogging(l *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			l.InfoContext(r.Context(), "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", RequestIDFromContext(r.Context())),
			)
		})
	}
}
```

- [ ] **Step 9: Write `internal/httpx/routes.go`**

```go
package httpx

import (
	"net/http"

	"go.opentelemetry.io/otel/trace"
)

// Route registers handler at pattern and renames the active server span to the
// route pattern. ServeMux populates r.Pattern only after routing, so the rename
// happens inside the handler rather than in an outer middleware, and the span
// name stays low cardinality.
//
// handler is an http.Handler so that per-route middleware composes without an
// adapter at every call site.
func Route(mux *http.ServeMux, pattern string, handler http.Handler) {
	mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace.SpanFromContext(r.Context()).SetName(pattern)
		handler.ServeHTTP(w, r)
	}))
}
```

- [ ] **Step 10: Run the middleware tests to verify they pass**

Run: `go test ./internal/httpx/... -v`
Expected: PASS, six tests.

- [ ] **Step 11: Write the failing server test**

`internal/httpx/server_test.go`:

```go
package httpx_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
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
```

- [ ] **Step 12: Write `internal/httpx/server.go`**

```go
package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Options configures a Server.
type Options struct {
	Addr            string
	Handler         http.Handler
	Logger          *slog.Logger
	ShutdownTimeout time.Duration
}

// Server is an HTTP server with a fixed middleware chain and graceful shutdown.
type Server struct {
	httpServer      *http.Server
	logger          *slog.Logger
	shutdownTimeout time.Duration
}

// NewServer wraps the handler in the standard chain: tracing outermost, then
// request id, request logging, and panic recovery innermost. Recovery sits
// inside logging so a recovered panic's 500 still reaches the access log.
func NewServer(o Options) *Server {
	handler := RequestLogging(o.Logger)(Recovery(o.Logger)(o.Handler))
	handler = RequestID(handler)
	handler = otelhttp.NewHandler(handler, "http.server")

	return &Server{
		httpServer: &http.Server{
			Addr:              o.Addr,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
		},
		logger:          o.Logger,
		shutdownTimeout: o.ShutdownTimeout,
	}
}

// Run serves until ctx is cancelled, then drains in-flight requests within the
// shutdown timeout. It returns nil on a clean shutdown.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.InfoContext(ctx, "http server listening", slog.String("addr", s.httpServer.Addr))
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()

	s.logger.InfoContext(shutdownCtx, "http server shutting down")
	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return <-errCh
}
```

`context.WithoutCancel` is what keeps the shutdown context alive after the
parent has already been cancelled. Deriving the shutdown timeout from the
cancelled parent would abort the drain immediately.

- [ ] **Step 13: Run the server test to verify it passes**

Run: `go test ./internal/httpx/... -race -v`
Expected: PASS, seven tests.

- [ ] **Step 14: Write `cmd/order/main.go`**

```go
// Command order runs the order service.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tunedev/go-resilient-commerce-lab/internal/config"
	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/logger"
	"github.com/tunedev/go-resilient-commerce-lab/internal/otelx"
)

const serviceName = "order"

func main() {
	if err := run(); err != nil {
		slog.Error("order service exited", slog.Any("error", err))
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(serviceName)
	if err != nil {
		return err
	}

	log := logger.New(os.Stdout, cfg.ServiceName, cfg.LogLevel)
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	providers, err := otelx.Setup(ctx, cfg.ServiceName, cfg.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := providers.Shutdown(flushCtx); err != nil {
			log.Error("telemetry shutdown", slog.Any("error", err))
		}
	}()

	mux := http.NewServeMux()
	httpx.Route(mux, "GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ok"})
	}))

	srv := httpx.NewServer(httpx.Options{
		Addr:            cfg.HTTPAddr,
		Handler:         mux,
		Logger:          log,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})

	return srv.Run(ctx)
}
```

- [ ] **Step 15: Run the binary by hand**

```bash
DATABASE_DSN=postgres://unused make run
```

In another terminal: `curl -i localhost:8080/healthz`
Expected: `200`, body `{"status":"ok"}`, an `X-Request-Id` header, and one JSON
log line on stdout carrying `"service":"order"`, `"status":200` and a `trace_id`.
Press Ctrl-C and confirm the process logs `http server shutting down` and exits 0.

- [ ] **Step 16: Commit**

```bash
git add internal/httpx cmd/order go.mod go.sum
git commit -m "feat(httpx): server shell with error envelope, middleware and graceful shutdown

Route names the server span after the route pattern rather than the concrete
path; ServeMux only fills r.Pattern after routing, so the rename happens in the
handler. Shutdown derives its deadline from context.WithoutCancel so draining
is not aborted by the cancellation that triggered it."
```

---

### Task 6: Dockerfile and the compose stack

**Closes:** PL-22, PL-23.

**Files:**
- Create: `.dockerignore`, `deploy/Dockerfile`, `deploy/docker-compose.yml`,
  `deploy/postgres/init.sql`, `deploy/otel-collector.yaml`,
  `deploy/prometheus.yml`,
  `deploy/grafana/provisioning/datasources/datasources.yml`

**Interfaces:**
- Consumes: `cmd/order` from Task 5.
- Produces: a running stack. Service hostnames inside the compose network are
  `postgres`, `otel-collector`, `jaeger`, `prometheus`, `grafana`, `order`.
  The order service's DSN is
  `postgres://lab:lab@postgres:5432/orders?sslmode=disable`.

- [ ] **Step 1: Write `deploy/Dockerfile`**

One file builds every binary. Distroless has no shell, so container healthchecks
that shell out do not work; readiness is polled over HTTP instead.

```dockerfile
ARG GO_VERSION=1.27.1

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG SERVICE
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${SERVICE}

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
```

- [ ] **Step 1b: Write `.dockerignore` at the repo root**

The build context is the repo root, so without this every `docs/` edit
invalidates the Docker layer cache, and from Task 14 onwards the context
carries a nested `go.mod` under `docs/`.

```
.git
.github
.superpowers
docs
deploy/grafana
*.md
```

- [ ] **Step 2: Write `deploy/postgres/init.sql`**

Runs once on first start, from `/docker-entrypoint-initdb.d`. Four separate
logical databases is what makes a cross-service transaction impossible rather
than merely discouraged.

```sql
CREATE DATABASE orders;
CREATE DATABASE inventory;
CREATE DATABASE payments;
CREATE DATABASE notifications;
```

- [ ] **Step 3: Write `deploy/otel-collector.yaml`**

Traces only. Metrics are scraped from each service directly.

```yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

processors:
  batch: {}

exporters:
  otlp/jaeger:
    endpoint: jaeger:4317
    tls:
      insecure: true

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp/jaeger]
```

- [ ] **Step 4: Write `deploy/prometheus.yml`**

```yaml
global:
  scrape_interval: 5s

scrape_configs:
  - job_name: order
    static_configs:
      - targets: ["order:8080"]
```

- [ ] **Step 5: Write `deploy/grafana/provisioning/datasources/datasources.yml`**

```yaml
apiVersion: 1

datasources:
  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://prometheus:9090
    isDefault: true
  - name: Jaeger
    type: jaeger
    access: proxy
    url: http://jaeger:16686
```

- [ ] **Step 6: Write `deploy/docker-compose.yml`**

`otel-collector` and `jaeger` both speak OTLP on 4317. Only the collector
publishes that port to the host; inside the network they are distinct hostnames,
so there is no conflict.

```yaml
name: grcl

services:
  postgres:
    image: postgres:18.6
    environment:
      POSTGRES_USER: lab
      POSTGRES_PASSWORD: lab
      POSTGRES_DB: lab
      # postgres 18 defaults PGDATA to /var/lib/postgresql/18/docker; this
      # pins it to the path the pgdata volume is mounted at.
      PGDATA: /var/lib/postgresql/data
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./postgres/init.sql:/docker-entrypoint-initdb.d/init.sql:ro
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U lab -d lab"]
      interval: 2s
      timeout: 3s
      retries: 30

  jaeger:
    image: jaegertracing/jaeger:2.20.0
    ports:
      - "16686:16686"

  otel-collector:
    image: otel/opentelemetry-collector-contrib:0.160.0
    command: ["--config=/etc/otel-collector.yaml"]
    volumes:
      - ./otel-collector.yaml:/etc/otel-collector.yaml:ro
    ports:
      - "4317:4317"
      - "4318:4318"
    depends_on:
      - jaeger

  prometheus:
    image: prom/prometheus:v3.14.0
    volumes:
      - ./prometheus.yml:/etc/prometheus/prometheus.yml:ro
    ports:
      - "9090:9090"

  grafana:
    image: grafana/grafana:13.2.1
    environment:
      GF_AUTH_ANONYMOUS_ENABLED: "true"
      GF_AUTH_ANONYMOUS_ORG_ROLE: Admin
      GF_AUTH_DISABLE_LOGIN_FORM: "true"
    volumes:
      - ./grafana/provisioning:/etc/grafana/provisioning:ro
    ports:
      - "3300:3000"
    depends_on:
      - prometheus

  order:
    build:
      context: ..
      dockerfile: deploy/Dockerfile
      args:
        SERVICE: order
    environment:
      HTTP_ADDR: ":8080"
      DATABASE_DSN: postgres://lab:lab@postgres:5432/orders?sslmode=disable
      OTEL_EXPORTER_OTLP_ENDPOINT: otel-collector:4317
      LOG_LEVEL: info
    ports:
      - "8080:8080"
    depends_on:
      postgres:
        condition: service_healthy
      otel-collector:
        condition: service_started

volumes:
  pgdata:
```

- [ ] **Step 7: Bring the stack up and verify it end to end**

```bash
make docker-up
curl -sf localhost:8080/healthz
```

Expected: `{"status":"ok"}`.

Then confirm the trace arrived: open `http://localhost:16686`, select service
`order`, and find a span named `GET /healthz`. If Jaeger lists no `order`
service, the trace pipeline is broken — check `docker compose logs otel-collector`
before proceeding. Do not continue to Task 7 with a broken trace path; every
later task assumes traces work.

Confirm Prometheus is up at `http://localhost:9090` (the `order` target will
report down until Task 12 adds `/metrics`; that is expected here) and Grafana at
`http://localhost:3300` shows both provisioned datasources.

- [ ] **Step 8: Tear down and commit**

```bash
make docker-down
git add deploy
git commit -m "feat(deploy): compose stack with four logical databases and trace pipeline

One Dockerfile parameterised by ARG SERVICE builds every binary. Postgres hosts
four separate databases so a cross-service transaction is impossible rather than
discouraged. The collector handles traces only; metrics are scraped per service."
```

---

### Task 7: internal/postgres — pool, migrations under a session lock, WithTx

**Closes:** PL-21, and the `integration` job of PL-31.

**Files:**
- Create: `internal/postgres/pool.go`, `internal/postgres/migrate.go`,
  `internal/postgres/tx.go`
- Create: `internal/postgres/tx_test.go`, `internal/postgres/migrate_test.go`
- Create: `internal/postgres/testing.go` (test helper, `integration` build tag)
- Modify: `.github/workflows/ci.yml` — add the `integration` job
- Modify: `Makefile` — already has the `integration` target from Task 1

**Interfaces:**
- Consumes: `config.Config.DatabaseDSN`.
- Produces:
  - `func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error)`
  - `func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error`
  - `func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error`
  - `func StartPostgres(t *testing.T) *pgxpool.Pool` (integration-tagged helper)

`WithTx` is the helper that makes "state row and outbox row in one transaction"
a single call at every repository site. Every later task depends on its exact
signature.

- [ ] **Step 1: Add the dependencies**

```bash
go get github.com/jackc/pgx/v5@v5.10.0
go get github.com/pressly/goose/v3@v3.28.0
go get github.com/testcontainers/testcontainers-go@v0.44.0
go get github.com/testcontainers/testcontainers-go/modules/postgres@v0.44.0
```

- [ ] **Step 2: Write the integration test helper**

`internal/postgres/testing.go`:

```go
//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartPostgres runs a throwaway Postgres and returns a pool connected to it.
// The container is terminated when the test finishes.
func StartPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:18.6",
		tcpostgres.WithDatabase("labtest"),
		tcpostgres.WithUsername("lab"),
		tcpostgres.WithPassword("lab"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("terminate postgres: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}
```

- [ ] **Step 3: Write the failing tests**

`internal/postgres/tx_test.go`:

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

func TestWithTxCommitsOnSuccess(t *testing.T) {
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE widgets (id text primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO widgets (id) VALUES ('a')`)
		return err
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE widgets (id text primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	sentinel := errors.New("business rule violated")
	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO widgets (id) VALUES ('a')`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("WithTx returned %v, want %v", err, sentinel)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d after rollback, want 0", count)
	}
}

func TestWithTxRollsBackOnPanic(t *testing.T) {
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE widgets (id text primary key)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic did not propagate")
			}
		}()
		_ = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO widgets (id) VALUES ('a')`); err != nil {
				return err
			}
			panic("boom")
		})
	}()

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d after panic, want 0", count)
	}
}
```

`internal/postgres/migrate_test.go`:

```go
//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

func migrations() fstest.MapFS {
	return fstest.MapFS{
		"00001_widgets.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (id text primary key);

-- +goose Down
DROP TABLE widgets;
`)},
	}
}

func TestMigrateAppliesMigrations(t *testing.T) {
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	if err := postgres.Migrate(ctx, pool, migrations()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'widgets')`,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !exists {
		t.Fatal("widgets table was not created")
	}
}

func TestMigrateIsSafeUnderConcurrentStartup(t *testing.T) {
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	// goose retries pg_try_advisory_lock on a five second interval, so each
	// loser waits a full interval before its next attempt. Three racers prove
	// the property; more only add wall clock.
	const replicas = 3
	errs := make(chan error, replicas)
	for range replicas {
		go func() { errs <- postgres.Migrate(ctx, pool, migrations()) }()
	}
	for range replicas {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Migrate: %v", err)
		}
	}

	var applied int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM goose_db_version WHERE version_id = 1`).Scan(&applied); err != nil {
		t.Fatalf("count applied: %v", err)
	}
	if applied != 1 {
		t.Errorf("migration recorded %d times, want 1", applied)
	}
}
```

- [ ] **Step 4: Run to verify failure**

Run: `go test -tags=integration ./internal/postgres/...`
Expected: FAIL — undefined `postgres.NewPool`, `postgres.Migrate`, `postgres.WithTx`.

- [ ] **Step 5: Write `internal/postgres/pool.go`**

```go
// Package postgres holds the database plumbing shared by every service.
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool opens a connection pool and verifies it can reach the database.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse dsn: %w", err)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: new pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}

	return pool, nil
}
```

Pool size is left at the pgx default and is deliberately configurable through
the DSN (`pool_max_conns`), because Epic H starves it on purpose.

- [ ] **Step 6: Write `internal/postgres/migrate.go`**

```go
package postgres

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Migrate applies every pending migration in fsys. It holds a Postgres session
// advisory lock for the duration, so replicas starting at the same time do not
// race each other.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("postgres: session locker: %w", err)
	}

	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		fsys,
		goose.WithSessionLocker(locker),
	)
	if err != nil {
		return fmt.Errorf("postgres: goose provider: %w", err)
	}

	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("postgres: migrate: %w", err)
	}
	return nil
}
```

`stdlib.OpenDBFromPool` reuses the existing pgxpool rather than opening a second
set of connections. goose needs a `*sql.DB`; pgx is the pool.

`fsys` is a flat filesystem of `.sql` files. Each service embeds its own from a
`migrations` package that sits in the same directory as the files, so no
`fs.Sub` is needed:

```go
//go:embed *.sql
var files embed.FS

// FS returns the embedded migration files.
func FS() fs.FS { return files }
```

- [ ] **Step 7: Write `internal/postgres/tx.go`**

```go
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WithTx runs fn inside a transaction, committing when fn returns nil and
// rolling back otherwise. A panic inside fn rolls back and then propagates.
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: begin: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: commit: %w", err)
	}
	committed = true
	return nil
}
```

The deferred rollback uses `context.WithoutCancel` so a cancelled request
context still releases the transaction rather than leaving it open until the
connection is reaped. Note that `fn`'s error is returned unwrapped, so callers
can use `errors.Is` against their own sentinels.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test -tags=integration -race ./internal/postgres/... -v`
Expected: PASS, five tests. Requires a working Docker daemon.
`TestMigrateIsSafeUnderConcurrentStartup` alone takes about ten seconds because
of goose's five-second lock retry interval; that is the test working, not
hanging.

- [ ] **Step 9: Add the integration job to CI**

Append to the `jobs:` block of `.github/workflows/ci.yml`:

```yaml
  integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: "1.27"
          cache: true
      - run: go test -race -tags=integration ./...
```

- [ ] **Step 10: Commit**

```bash
git add internal/postgres .github/workflows/ci.yml go.mod go.sum
git commit -m "feat(postgres): pool, goose migrations under a session lock, WithTx

Migrations run at startup holding a Postgres session advisory lock, so several
replicas starting together apply each migration once. WithTx returns fn's error
unwrapped so callers can match their own sentinels with errors.Is."
```

---

### Task 8: order domain — statuses and transitions

**Closes:** PL-24.

**Files:**
- Create: `services/order/domain/order.go`, `services/order/domain/errors.go`
- Create: `services/order/domain/order_test.go`

**Interfaces:**
- Consumes: nothing. This package imports no driver, no `net/http`, nothing
  from `adapter/`. Enforce it: the only non-stdlib import permitted here is none.
- Produces:
  - `type Status string` with the ten constants below
  - `type Item struct { SKU string; Quantity int; UnitPrice int64 }`
  - `type Order struct { ID, CustomerID string; Status Status; TotalAmount int64; Currency, PaymentMethodID string; Items []Item; CreatedAt, UpdatedAt time.Time }`
  - `func (s Status) CanTransitionTo(next Status) bool`
  - `func (o *Order) TransitionTo(next Status) error`
  - `func Validate(customerID, paymentMethodID string, items []Item) error`
  - Sentinels: `ErrIllegalTransition`, `ErrMissingCustomer`, `ErrMissingPaymentMethod`, `ErrNoItems`, `ErrInvalidQuantity`, `ErrOrderNotFound`

**Correction to story PL-24's wording:** a SQL `CHECK` constraint cannot see the
previous value of a column, so it can enforce the *set* of valid statuses but not
the legal *edges*. Task 9 adds the status-domain CHECK. The transition guard is a
`BEFORE UPDATE` trigger and belongs in Epic D, where transitions first happen —
Epic A only ever writes `pending`. Building the trigger now would guard nothing.

- [ ] **Step 1: Write the failing tests**

`services/order/domain/order_test.go`:

```go
package domain_test

import (
	"errors"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

func TestLegalTransitions(t *testing.T) {
	legal := []struct{ from, to domain.Status }{
		{domain.StatusPending, domain.StatusInventoryReserved},
		{domain.StatusPending, domain.StatusCancelled},
		{domain.StatusPending, domain.StatusExpired},
		{domain.StatusInventoryReserved, domain.StatusPaymentPending},
		{domain.StatusInventoryReserved, domain.StatusCancelled},
		{domain.StatusPaymentPending, domain.StatusPaymentSucceeded},
		{domain.StatusPaymentPending, domain.StatusPaymentFailed},
		{domain.StatusPaymentPending, domain.StatusAwaitingReconciliation},
		{domain.StatusPaymentSucceeded, domain.StatusConfirmed},
		{domain.StatusPaymentSucceeded, domain.StatusManualReview},
		{domain.StatusPaymentFailed, domain.StatusCancelled},
		{domain.StatusAwaitingReconciliation, domain.StatusPaymentSucceeded},
		{domain.StatusAwaitingReconciliation, domain.StatusPaymentFailed},
		{domain.StatusAwaitingReconciliation, domain.StatusManualReview},
	}

	for _, tc := range legal {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			if !tc.from.CanTransitionTo(tc.to) {
				t.Errorf("%s -> %s rejected, want allowed", tc.from, tc.to)
			}
		})
	}
}

func TestIllegalTransitions(t *testing.T) {
	illegal := []struct{ from, to domain.Status }{
		{domain.StatusPending, domain.StatusConfirmed},
		{domain.StatusPending, domain.StatusPaymentSucceeded},
		{domain.StatusConfirmed, domain.StatusCancelled},
		{domain.StatusCancelled, domain.StatusPending},
		{domain.StatusExpired, domain.StatusConfirmed},
		{domain.StatusManualReview, domain.StatusConfirmed},
		{domain.StatusAwaitingReconciliation, domain.StatusCancelled},
	}

	for _, tc := range illegal {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			if tc.from.CanTransitionTo(tc.to) {
				t.Errorf("%s -> %s allowed, want rejected", tc.from, tc.to)
			}
		})
	}
}

func TestAwaitingReconciliationNeverReachesCancelled(t *testing.T) {
	if domain.StatusAwaitingReconciliation.CanTransitionTo(domain.StatusCancelled) {
		t.Fatal("an unknown payment must never be cancellable; releasing inventory " +
			"while a charge may have landed is the bug this lab exists to demonstrate")
	}
}

func TestTransitionToUpdatesStatus(t *testing.T) {
	o := &domain.Order{Status: domain.StatusPending}

	if err := o.TransitionTo(domain.StatusInventoryReserved); err != nil {
		t.Fatalf("TransitionTo: %v", err)
	}
	if o.Status != domain.StatusInventoryReserved {
		t.Errorf("Status = %q, want %q", o.Status, domain.StatusInventoryReserved)
	}
}

func TestTransitionToRejectsIllegalEdge(t *testing.T) {
	o := &domain.Order{Status: domain.StatusPending}

	err := o.TransitionTo(domain.StatusConfirmed)
	if !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
	if o.Status != domain.StatusPending {
		t.Errorf("Status changed to %q on a rejected transition", o.Status)
	}
}

func TestValidate(t *testing.T) {
	good := []domain.Item{{SKU: "playstation-5", Quantity: 1, UnitPrice: 150000}}

	cases := []struct {
		name       string
		customerID string
		method     string
		items      []domain.Item
		want       error
	}{
		{"valid", "cust_1", "pm_ok", good, nil},
		{"no customer", "", "pm_ok", good, domain.ErrMissingCustomer},
		{"no payment method", "cust_1", "", good, domain.ErrMissingPaymentMethod},
		{"no items", "cust_1", "pm_ok", nil, domain.ErrNoItems},
		{"zero quantity", "cust_1", "pm_ok",
			[]domain.Item{{SKU: "playstation-5", Quantity: 0}}, domain.ErrInvalidQuantity},
		{"negative quantity", "cust_1", "pm_ok",
			[]domain.Item{{SKU: "playstation-5", Quantity: -1}}, domain.ErrInvalidQuantity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.Validate(tc.customerID, tc.method, tc.items)
			if !errors.Is(err, tc.want) {
				t.Errorf("Validate = %v, want %v", err, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./services/order/domain/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write `services/order/domain/errors.go`**

```go
package domain

import "errors"

var (
	// ErrIllegalTransition is returned for an edge not present in the state machine.
	ErrIllegalTransition = errors.New("illegal order status transition")
	// ErrMissingCustomer is returned when customer_id is empty.
	ErrMissingCustomer = errors.New("customer_id is required")
	// ErrMissingPaymentMethod is returned when payment_method_id is empty.
	ErrMissingPaymentMethod = errors.New("payment_method_id is required")
	// ErrNoItems is returned when an order has no line items.
	ErrNoItems = errors.New("at least one item is required")
	// ErrInvalidQuantity is returned when a line item quantity is not positive.
	ErrInvalidQuantity = errors.New("item quantity must be positive")
	// ErrOrderNotFound is returned when no order exists for an id.
	ErrOrderNotFound = errors.New("order not found")
)
```

- [ ] **Step 4: Write `services/order/domain/order.go`**

```go
// Package domain holds the order state machine. It performs no I/O.
package domain

import (
	"fmt"
	"time"
)

// Status is the lifecycle state of an order.
type Status string

// The order lifecycle.
const (
	StatusPending                Status = "pending"
	StatusInventoryReserved      Status = "inventory_reserved"
	StatusPaymentPending         Status = "payment_pending"
	StatusPaymentSucceeded       Status = "payment_succeeded"
	StatusPaymentFailed          Status = "payment_failed"
	StatusConfirmed              Status = "confirmed"
	StatusCancelled              Status = "cancelled"
	StatusExpired                Status = "expired"
	StatusAwaitingReconciliation Status = "awaiting_reconciliation"
	StatusManualReview           Status = "manual_review"
)

// AllStatuses is the full status domain, used to build the database CHECK constraint.
var AllStatuses = []Status{
	StatusPending, StatusInventoryReserved, StatusPaymentPending,
	StatusPaymentSucceeded, StatusPaymentFailed, StatusConfirmed,
	StatusCancelled, StatusExpired, StatusAwaitingReconciliation,
	StatusManualReview,
}

// transitions is the complete set of legal edges. A status absent as a key is
// terminal. StatusAwaitingReconciliation deliberately has no edge to
// StatusCancelled: an unknown payment must never trigger compensation.
var transitions = map[Status][]Status{
	StatusPending:                {StatusInventoryReserved, StatusCancelled, StatusExpired},
	StatusInventoryReserved:      {StatusPaymentPending, StatusCancelled},
	StatusPaymentPending:         {StatusPaymentSucceeded, StatusPaymentFailed, StatusAwaitingReconciliation},
	StatusPaymentSucceeded:       {StatusConfirmed, StatusManualReview},
	StatusPaymentFailed:          {StatusCancelled},
	StatusAwaitingReconciliation: {StatusPaymentSucceeded, StatusPaymentFailed, StatusManualReview},
}

// CanTransitionTo reports whether from -> next is a legal edge.
func (s Status) CanTransitionTo(next Status) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// Item is one line of an order.
type Item struct {
	SKU       string
	Quantity  int
	UnitPrice int64
}

// Order is the aggregate root.
type Order struct {
	ID              string
	CustomerID      string
	Status          Status
	TotalAmount     int64
	Currency        string
	PaymentMethodID string
	Items           []Item
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// TransitionTo moves the order to next, or returns ErrIllegalTransition and
// leaves the order unchanged.
func (o *Order) TransitionTo(next Status) error {
	if !o.Status.CanTransitionTo(next) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, o.Status, next)
	}
	o.Status = next
	return nil
}

// Total returns the sum of the line items in minor currency units.
func Total(items []Item) int64 {
	var total int64
	for _, it := range items {
		total += it.UnitPrice * int64(it.Quantity)
	}
	return total
}

// Validate checks the fields a caller supplies when creating an order.
func Validate(customerID, paymentMethodID string, items []Item) error {
	if customerID == "" {
		return ErrMissingCustomer
	}
	if paymentMethodID == "" {
		return ErrMissingPaymentMethod
	}
	if len(items) == 0 {
		return ErrNoItems
	}
	for _, it := range items {
		if it.Quantity <= 0 {
			return fmt.Errorf("%w: sku %s", ErrInvalidQuantity, it.SKU)
		}
	}
	return nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./services/order/domain/... -v`
Expected: PASS. `TestAwaitingReconciliationNeverReachesCancelled` is the one
that matters most; it is the first of the four layers from spec §5.7.

- [ ] **Step 6: Verify the package has no infrastructure imports**

Run: `go list -deps ./services/order/domain | grep -vE '^(internal/|[a-z/]+$)' | head`
Expected: no `github.com/jackc`, no `net/http`, no `go.opentelemetry.io`.

- [ ] **Step 7: Commit**

```bash
git add services/order/domain
git commit -m "feat(order): order state machine as pure functions

awaiting_reconciliation has no edge to cancelled: an unknown payment must never
reach compensation, because releasing inventory while a charge may have landed
is the failure this lab exists to demonstrate."
```

---

### Task 9: internal/outbox — transactional write and SKIP LOCKED publisher

**Closes:** PL-29.

**Ordering note.** Outbox and idempotency are built before the order service so
that `OrderStore.CreateOrder` is written once against its final signature,
rather than churned twice.

**Files:**
- Create: `internal/outbox/event.go`, `internal/outbox/store.go`,
  `internal/outbox/sink.go`, `internal/outbox/publisher.go`
- Create: `internal/outbox/store_test.go`, `internal/outbox/publisher_test.go`

**Interfaces:**
- Consumes: `postgres.WithTx`, `postgres.StartPostgres`.
- Produces:
  - `type Event struct { ID uuid.UUID; AggregateType, AggregateID, EventType string; Payload json.RawMessage }`
  - `func NewEvent(aggregateType, aggregateID, eventType string, payload any) (Event, error)`
  - `func Append(ctx context.Context, tx pgx.Tx, events ...Event) error`
  - `type Sink interface { Publish(ctx context.Context, e Event) error }`
  - `type LogSink struct { Logger *slog.Logger }`
  - `func NewPublisher(pool *pgxpool.Pool, sink Sink, logger *slog.Logger, opts PublisherOptions) *Publisher`
  - `type PublisherOptions struct { Interval time.Duration; BatchSize int; MaxRetries int; BaseBackoff time.Duration }`
  - `func (p *Publisher) Run(ctx context.Context) error`
  - `const Schema` — the `outbox_events` DDL, duplicated verbatim into each
    service's own migrations. The duplication is intended: the four services own
    four separate databases with four separate migration histories.

Backoff here is plain exponential with no jitter. Epic F replaces it with
`internal/retry`, which adds jitter and retry classification.

- [ ] **Step 1: Write `internal/outbox/event.go`**

```go
// Package outbox stores domain events in the same transaction as the state
// change that produced them, and publishes them afterwards.
package outbox

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// Schema is the outbox_events DDL. Each service copies it into its own
// migrations; the services own separate databases and separate histories.
const Schema = `
CREATE TABLE outbox_events (
    id              uuid PRIMARY KEY,
    aggregate_type  text        NOT NULL,
    aggregate_id    text        NOT NULL,
    event_type      text        NOT NULL,
    payload         jsonb       NOT NULL,
    status          text        NOT NULL DEFAULT 'pending',
    retry_count     integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    published_at    timestamptz,
    last_error      text,
    CONSTRAINT outbox_events_status_valid
        CHECK (status IN ('pending','published','retrying','dead_lettered'))
);

CREATE INDEX outbox_events_claimable
    ON outbox_events (next_attempt_at)
    WHERE status IN ('pending','retrying');
`

// Status values for an outbox row.
const (
	StatusPending      = "pending"
	StatusPublished    = "published"
	StatusRetrying     = "retrying"
	StatusDeadLettered = "dead_lettered"
)

// Event is one domain event awaiting publication.
type Event struct {
	ID            uuid.UUID
	AggregateType string
	AggregateID   string
	EventType     string
	Payload       json.RawMessage
}

// NewEvent marshals payload and assigns the event id. The id is stable for the
// life of the row and becomes the deduplication key for consumers.
func NewEvent(aggregateType, aggregateID, eventType string, payload any) (Event, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return Event{}, fmt.Errorf("outbox: marshal %s payload: %w", eventType, err)
	}
	return Event{
		ID:            uuid.New(),
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     eventType,
		Payload:       body,
	}, nil
}
```

- [ ] **Step 2: Write the failing store test**

`internal/outbox/store_test.go`:

```go
//go:build integration

package outbox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

func setup(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool := postgres.StartPostgres(t)

	if _, err := pool.Exec(ctx, `CREATE TABLE widgets (id text primary key)`); err != nil {
		t.Fatalf("create widgets: %v", err)
	}
	if _, err := pool.Exec(ctx, outbox.Schema); err != nil {
		t.Fatalf("create outbox_events: %v", err)
	}
	return ctx, pool
}

func TestAppendCommitsWithTheStateChange(t *testing.T) {
	ctx, pool := setup(t)

	event, err := outbox.NewEvent("order", "ord_1", "OrderCreated", map[string]string{"order_id": "ord_1"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO widgets (id) VALUES ('ord_1')`); err != nil {
			return err
		}
		return outbox.Append(ctx, tx, event)
	})
	if err != nil {
		t.Fatalf("WithTx: %v", err)
	}

	var widgets, events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&widgets); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if widgets != 1 || events != 1 {
		t.Fatalf("widgets = %d, events = %d, want 1 and 1", widgets, events)
	}
}

func TestAppendIsRolledBackWithTheStateChange(t *testing.T) {
	ctx, pool := setup(t)

	event, err := outbox.NewEvent("order", "ord_1", "OrderCreated", map[string]string{"order_id": "ord_1"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	sentinel := errors.New("business rule violated")
	err = postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO widgets (id) VALUES ('ord_1')`); err != nil {
			return err
		}
		if err := outbox.Append(ctx, tx, event); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}

	var widgets, events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM widgets`).Scan(&widgets); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM outbox_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if widgets != 0 || events != 0 {
		t.Fatalf("widgets = %d, events = %d after rollback, want 0 and 0", widgets, events)
	}
}
```

Add `"github.com/jackc/pgx/v5/pgxpool"` to the imports.

This pair is the whole point of the outbox: neither row can exist without the
other. Losing this test loses the guarantee.

- [ ] **Step 3: Write `internal/outbox/store.go`**

```go
package outbox

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Append writes events inside the caller's transaction, so they commit with the
// state change that produced them or not at all.
func Append(ctx context.Context, tx pgx.Tx, events ...Event) error {
	const query = `
INSERT INTO outbox_events (id, aggregate_type, aggregate_id, event_type, payload)
VALUES ($1, $2, $3, $4, $5)`

	for _, e := range events {
		_, err := tx.Exec(ctx, query, e.ID, e.AggregateType, e.AggregateID, e.EventType, e.Payload)
		if err != nil {
			return fmt.Errorf("outbox: append %s: %w", e.EventType, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the store tests to verify they pass**

Run: `go test -tags=integration ./internal/outbox/... -run TestAppend -v`
Expected: PASS, two tests.

- [ ] **Step 5: Write `internal/outbox/sink.go`**

```go
package outbox

import (
	"context"
	"log/slog"
)

// Sink delivers a published event. Epic E adds a Kafka implementation; changing
// transport is a one-line change at the composition root.
type Sink interface {
	Publish(ctx context.Context, e Event) error
}

// LogSink records the event and reports success. It is the only sink until
// Redpanda arrives in Epic E.
type LogSink struct {
	Logger *slog.Logger
}

// Publish logs the event.
func (s LogSink) Publish(ctx context.Context, e Event) error {
	s.Logger.InfoContext(ctx, "outbox event published",
		slog.String("event_id", e.ID.String()),
		slog.String("event_type", e.EventType),
		slog.String("aggregate_type", e.AggregateType),
		slog.String("aggregate_id", e.AggregateID),
	)
	return nil
}
```

- [ ] **Step 6: Write the failing publisher tests**

`internal/outbox/publisher_test.go`:

```go
//go:build integration

package outbox_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/tunedev/go-resilient-commerce-lab/internal/logger"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

type recordingSink struct {
	mu    sync.Mutex
	seen  []outbox.Event
	fail  error
}

func (s *recordingSink) Publish(_ context.Context, e outbox.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return s.fail
	}
	s.seen = append(s.seen, e)
	return nil
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

func seedEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventType string) {
	t.Helper()
	e, err := outbox.NewEvent("order", "ord_1", eventType, map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return outbox.Append(ctx, tx, e)
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func newPublisher(pool *pgxpool.Pool, sink outbox.Sink, maxRetries int) *outbox.Publisher {
	var buf bytes.Buffer
	return outbox.NewPublisher(pool, sink, logger.New(&buf, "test", slog.LevelError),
		outbox.PublisherOptions{
			Interval:    10 * time.Millisecond,
			BatchSize:   10,
			MaxRetries:  maxRetries,
			BaseBackoff: time.Millisecond,
		})
}

func TestPublisherMarksEventsPublished(t *testing.T) {
	ctx, pool := setup(t)
	seedEvent(t, ctx, pool, "OrderCreated")

	sink := &recordingSink{}
	p := newPublisher(pool, sink, 3)

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()

	waitFor(t, func() bool { return sink.count() == 1 })
	cancel()
	<-done

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM outbox_events`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != outbox.StatusPublished {
		t.Errorf("status = %q, want %q", status, outbox.StatusPublished)
	}
}

func TestPublisherDeadLettersAfterMaxRetries(t *testing.T) {
	ctx, pool := setup(t)
	seedEvent(t, ctx, pool, "OrderCreated")

	sink := &recordingSink{fail: errors.New("sink is down")}
	p := newPublisher(pool, sink, 2)

	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()

	waitFor(t, func() bool {
		var status string
		if err := pool.QueryRow(ctx, `SELECT status FROM outbox_events`).Scan(&status); err != nil {
			return false
		}
		return status == outbox.StatusDeadLettered
	})
	cancel()
	<-done

	var lastError string
	if err := pool.QueryRow(ctx, `SELECT coalesce(last_error, '') FROM outbox_events`).Scan(&lastError); err != nil {
		t.Fatal(err)
	}
	if lastError == "" {
		t.Error("dead lettered without recording a reason")
	}
}

func TestConcurrentPublishersDoNotDoublePublish(t *testing.T) {
	ctx, pool := setup(t)
	for range 20 {
		seedEvent(t, ctx, pool, "OrderCreated")
	}

	sink := &recordingSink{}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for range 4 {
		p := newPublisher(pool, sink, 3)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.Run(runCtx)
		}()
	}

	waitFor(t, func() bool { return sink.count() >= 20 })
	cancel()
	wg.Wait()

	if got := sink.count(); got != 20 {
		t.Fatalf("sink saw %d publishes for 20 events; a claimed row was published twice", got)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before deadline")
}
```

Add `"github.com/jackc/pgx/v5/pgxpool"` to the imports here too; `seedEvent`
and `newPublisher` both take one.

`TestConcurrentPublishersDoNotDoublePublish` proves that concurrent publishers
never publish a row twice. It does **not** isolate `SKIP LOCKED`: dropping that
clause leaves the test green, because plain `FOR UPDATE` blocks the second
publisher until the first commits, and the blocked query then re-evaluates its
`WHERE` clause under READ COMMITTED and no longer matches the now-published row.

What `SKIP LOCKED` actually buys is liveness, not correctness. Without it,
replicas serialise behind each other on the same rows instead of taking disjoint
batches in parallel. Correctness comes from claiming and marking the row inside
one transaction.

- [ ] **Step 7: Run to verify failure**

Run: `go test -tags=integration ./internal/outbox/... -run TestPublisher`
Expected: FAIL — undefined `outbox.NewPublisher`.

- [ ] **Step 8: Write `internal/outbox/publisher.go`**

```go
package outbox

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

// PublisherOptions configures the publish loop.
type PublisherOptions struct {
	Interval    time.Duration
	BatchSize   int
	MaxRetries  int
	BaseBackoff time.Duration
}

// Publisher drains outbox_events into a Sink.
type Publisher struct {
	pool   *pgxpool.Pool
	sink   Sink
	logger *slog.Logger
	opts   PublisherOptions
}

// NewPublisher builds a publisher. Several may run concurrently against the
// same table.
func NewPublisher(pool *pgxpool.Pool, sink Sink, logger *slog.Logger, opts PublisherOptions) *Publisher {
	return &Publisher{pool: pool, sink: sink, logger: logger, opts: opts}
}

// Run drains the outbox until ctx is cancelled, then returns nil.
func (p *Publisher) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.opts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := p.drainBatch(ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.logger.ErrorContext(ctx, "outbox drain", slog.Any("error", err))
			}
		}
	}
}

type claimed struct {
	Event      Event
	RetryCount int
}

// drainBatch claims one batch and publishes it. The whole batch is handled
// inside a single transaction so the row locks are held for the publish
// attempt, which is what stops a second publisher taking the same rows.
func (p *Publisher) drainBatch(ctx context.Context) error {
	return postgres.WithTx(ctx, p.pool, func(tx pgx.Tx) error {
		batch, err := claim(ctx, tx, p.opts.BatchSize)
		if err != nil {
			return err
		}
		for _, c := range batch {
			if err := p.sink.Publish(ctx, c.Event); err != nil {
				if err := p.recordFailure(ctx, tx, c, err); err != nil {
					return err
				}
				continue
			}
			if err := markPublished(ctx, tx, c.Event.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func claim(ctx context.Context, tx pgx.Tx, batchSize int) ([]claimed, error) {
	const query = `
SELECT id, aggregate_type, aggregate_id, event_type, payload, retry_count
  FROM outbox_events
 WHERE status IN ('pending', 'retrying')
   AND next_attempt_at <= now()
 ORDER BY created_at
 FOR UPDATE SKIP LOCKED
 LIMIT $1`

	rows, err := tx.Query(ctx, query, batchSize)
	if err != nil {
		return nil, fmt.Errorf("outbox: claim: %w", err)
	}
	defer rows.Close()

	var batch []claimed
	for rows.Next() {
		var c claimed
		if err := rows.Scan(&c.Event.ID, &c.Event.AggregateType, &c.Event.AggregateID,
			&c.Event.EventType, &c.Event.Payload, &c.RetryCount); err != nil {
			return nil, fmt.Errorf("outbox: scan claimed: %w", err)
		}
		batch = append(batch, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("outbox: claim rows: %w", err)
	}
	return batch, nil
}

func markPublished(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	const query = `
UPDATE outbox_events
   SET status = 'published', published_at = now(), last_error = NULL
 WHERE id = $1`

	if _, err := tx.Exec(ctx, query, id); err != nil {
		return fmt.Errorf("outbox: mark published: %w", err)
	}
	return nil
}

func (p *Publisher) recordFailure(ctx context.Context, tx pgx.Tx, c claimed, cause error) error {
	attempt := c.RetryCount + 1

	status := StatusRetrying
	if attempt >= p.opts.MaxRetries {
		status = StatusDeadLettered
		p.logger.ErrorContext(ctx, "outbox event dead lettered",
			slog.String("event_id", c.Event.ID.String()),
			slog.String("event_type", c.Event.EventType),
			slog.Int("attempts", attempt),
			slog.Any("error", cause),
		)
	}

	const query = `
UPDATE outbox_events
   SET status = $2, retry_count = $3, last_error = $4, next_attempt_at = now() + $5::interval
 WHERE id = $1`

	backoff := p.opts.BaseBackoff << min(attempt-1, 16)
	_, err := tx.Exec(ctx, query, c.Event.ID, status, attempt, cause.Error(), backoff.String())
	if err != nil {
		return fmt.Errorf("outbox: record failure: %w", err)
	}
	return nil
}
```

Note that `drainBatch` returns `nil` from the transaction even when a publish
failed: the failure has been recorded on the row, and returning an error would
roll that record back and lose the retry count.

- [ ] **Step 9: Run the publisher tests to verify they pass**

Run: `go test -tags=integration -race ./internal/outbox/... -v`
Expected: PASS, five tests.

- [ ] **Step 10: Commit**

```bash
git add internal/outbox
git commit -m "feat(outbox): transactional append and SKIP LOCKED publisher

FOR UPDATE SKIP LOCKED lets several publisher replicas take disjoint batches
instead of serialising behind each other. It is a liveness property: correctness
comes from claiming and marking a row inside one transaction, which plain FOR
UPDATE also preserves. drainBatch commits the failure record rather than
returning the publish error, so a failed attempt does not roll back its own
retry count."
```

---

### Task 10: internal/idempotency — claim, replay, conflict, complete

**Closes:** PL-26.

**Files:**
- Create: `internal/idempotency/hash.go`, `internal/idempotency/store.go`,
  `internal/idempotency/middleware.go`
- Create: `internal/idempotency/hash_test.go`, `internal/idempotency/store_test.go`,
  `internal/idempotency/middleware_test.go`

**Interfaces:**
- Consumes: `postgres.WithTx`, `httpx.WriteError`, `httpx.WriteJSON`.
- Produces:
  - `const Schema` — the `idempotency_keys` DDL
  - `func Hash(body []byte) (string, error)`
  - `type Outcome int` with `OutcomeOwned`, `OutcomeReplay`, `OutcomeInProgress`, `OutcomeConflict`
  - `type Record struct { Key, RequestHash string; State string; ResourceType, ResourceID string; ResponseBody []byte; StatusCode int }`
  - `type Store struct{...}`, `func NewStore(pool *pgxpool.Pool) *Store`
  - `func (s *Store) Claim(ctx context.Context, key, requestHash string) (Outcome, Record, error)`
  - `func (s *Store) Release(ctx context.Context, key string) error`
  - `type Completion struct { Key, ResourceType, ResourceID string; StatusCode int; ResponseBody []byte }`
  - `func Complete(ctx context.Context, tx pgx.Tx, c Completion) error`
  - `func Require(store *Store) func(http.Handler) http.Handler`
  - `func KeyFromContext(ctx context.Context) (string, bool)`

**The design, stated once so no step has to re-derive it.**

The middleware cannot join the handler's transaction, and the handler's response
is not known until after it runs. Threading response bytes back out through the
layers, or writing the completion in a second transaction, both produce a window
where the order exists and the key does not. So the work is split:

1. **Middleware, own transaction, before the handler.** `INSERT ... ON CONFLICT
   DO NOTHING` claims the key with state `in_progress`. Inserting a row means we
   own the request. Not inserting means someone else got there, so read the
   existing row and decide: hash mismatch is `409`, `completed` is a replay of
   the stored body and status, `in_progress` is `202`.
2. **Use case, inside its own transaction, with the order.** `Complete` flips the
   claimed row to `completed` and stores the resource id, status code and
   response body. The order row and the completed key therefore commit together.
3. **Middleware, after the handler.** If the handler produced `5xx`, release the
   claim so a retry can proceed.

A crash between step 1 and step 2 leaves an `in_progress` row with no order. A
retry gets `202`, which is honest — the system genuinely does not know yet. That
is what the `in_progress` state exists for.

- [ ] **Step 1: Write the failing hash tests**

`internal/idempotency/hash_test.go`:

```go
package idempotency_test

import (
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
)

func TestHashIgnoresKeyOrderAndWhitespace(t *testing.T) {
	a, err := idempotency.Hash([]byte(`{"customer_id":"c1","payment_method_id":"pm_ok"}`))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	b, err := idempotency.Hash([]byte("{\n  \"payment_method_id\": \"pm_ok\",\n  \"customer_id\": \"c1\"\n}"))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}

	if a != b {
		t.Errorf("reordered and reformatted bodies hashed differently:\n%s\n%s", a, b)
	}
}

func TestHashDistinguishesDifferentValues(t *testing.T) {
	a, _ := idempotency.Hash([]byte(`{"customer_id":"c1"}`))
	b, _ := idempotency.Hash([]byte(`{"customer_id":"c2"}`))

	if a == b {
		t.Error("different bodies hashed identically")
	}
}

func TestHashRejectsMalformedJSON(t *testing.T) {
	if _, err := idempotency.Hash([]byte(`{`)); err == nil {
		t.Error("Hash accepted malformed JSON")
	}
}
```

- [ ] **Step 2: Write `internal/idempotency/hash.go`**

```go
// Package idempotency makes a retried request safe to repeat.
package idempotency

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Hash returns a canonical digest of a JSON request body. Decoding and
// re-encoding normalises key order and whitespace, so a client that
// reserialises an identical request does not trip a false conflict.
func Hash(body []byte) (string, error) {
	var canonical any
	if err := json.Unmarshal(body, &canonical); err != nil {
		return "", fmt.Errorf("idempotency: canonicalise request: %w", err)
	}

	normalised, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("idempotency: re-encode request: %w", err)
	}

	sum := sha256.Sum256(normalised)
	return hex.EncodeToString(sum[:]), nil
}
```

Numbers pass through `float64`, so integers beyond 2^53 would lose precision.
No field in this API carries such a value; revisit if one ever does.

- [ ] **Step 3: Run the hash tests to verify they pass**

Run: `go test ./internal/idempotency/... -run TestHash -v`
Expected: PASS, three tests.

- [ ] **Step 4: Write the failing store tests**

`internal/idempotency/store_test.go`:

```go
//go:build integration

package idempotency_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

func store(t *testing.T) (context.Context, *pgxpool.Pool, *idempotency.Store) {
	t.Helper()
	ctx := context.Background()
	pool := postgres.StartPostgres(t)
	if _, err := pool.Exec(ctx, idempotency.Schema); err != nil {
		t.Fatalf("create idempotency_keys: %v", err)
	}
	return ctx, pool, idempotency.NewStore(pool)
}

func TestClaimOwnsAFreshKey(t *testing.T) {
	ctx, _, s := store(t)

	outcome, _, err := s.Claim(ctx, "key-1", "hash-a")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if outcome != idempotency.OutcomeOwned {
		t.Errorf("outcome = %v, want OutcomeOwned", outcome)
	}
}

func TestSecondClaimWhileInProgress(t *testing.T) {
	ctx, _, s := store(t)

	if _, _, err := s.Claim(ctx, "key-1", "hash-a"); err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	outcome, _, err := s.Claim(ctx, "key-1", "hash-a")
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if outcome != idempotency.OutcomeInProgress {
		t.Errorf("outcome = %v, want OutcomeInProgress", outcome)
	}
}

func TestClaimWithDifferentHashConflicts(t *testing.T) {
	ctx, _, s := store(t)

	if _, _, err := s.Claim(ctx, "key-1", "hash-a"); err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	outcome, _, err := s.Claim(ctx, "key-1", "hash-b")
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if outcome != idempotency.OutcomeConflict {
		t.Errorf("outcome = %v, want OutcomeConflict", outcome)
	}
}

func TestClaimAfterCompletionReplays(t *testing.T) {
	ctx, pool, s := store(t)
	body := []byte(`{"order_id":"ord_1","status":"pending"}`)

	if _, _, err := s.Claim(ctx, "key-1", "hash-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return idempotency.Complete(ctx, tx, idempotency.Completion{
			Key:          "key-1",
			ResourceType: "order",
			ResourceID:   "ord_1",
			StatusCode:   http.StatusAccepted,
			ResponseBody: body,
		})
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	outcome, rec, err := s.Claim(ctx, "key-1", "hash-a")
	if err != nil {
		t.Fatalf("Claim after completion: %v", err)
	}
	if outcome != idempotency.OutcomeReplay {
		t.Fatalf("outcome = %v, want OutcomeReplay", outcome)
	}
	if rec.StatusCode != http.StatusAccepted {
		t.Errorf("StatusCode = %d, want 202", rec.StatusCode)
	}
	if string(rec.ResponseBody) != string(body) {
		t.Errorf("ResponseBody = %s, want %s", rec.ResponseBody, body)
	}
}

func TestReleaseAllowsRetry(t *testing.T) {
	ctx, _, s := store(t)

	if _, _, err := s.Claim(ctx, "key-1", "hash-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if err := s.Release(ctx, "key-1"); err != nil {
		t.Fatalf("Release: %v", err)
	}

	outcome, _, err := s.Claim(ctx, "key-1", "hash-a")
	if err != nil {
		t.Fatalf("Claim after release: %v", err)
	}
	if outcome != idempotency.OutcomeOwned {
		t.Errorf("outcome = %v, want OutcomeOwned", outcome)
	}
}

func TestCompleteRefusesAnUnclaimedKey(t *testing.T) {
	ctx, pool, _ := store(t)

	err := postgres.WithTx(ctx, pool, func(tx pgx.Tx) error {
		return idempotency.Complete(ctx, tx, idempotency.Completion{
			Key: "never-claimed", ResourceType: "order", ResourceID: "ord_1", StatusCode: 202,
			ResponseBody: []byte(`{}`),
		})
	})
	if err == nil {
		t.Fatal("Complete succeeded for a key that was never claimed")
	}
}

func TestConcurrentClaimsElectOneOwner(t *testing.T) {
	ctx, _, s := store(t)

	const racers = 16
	outcomes := make(chan idempotency.Outcome, racers)
	for range racers {
		go func() {
			outcome, _, err := s.Claim(ctx, "key-1", "hash-a")
			if err != nil {
				outcomes <- idempotency.OutcomeConflict
				return
			}
			outcomes <- outcome
		}()
	}

	owners := 0
	for range racers {
		if <-outcomes == idempotency.OutcomeOwned {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("%d goroutines claimed ownership, want exactly 1", owners)
	}
}
```

- [ ] **Step 5: Run to verify failure**

Run: `go test -tags=integration ./internal/idempotency/...`
Expected: FAIL — undefined `idempotency.Schema`, `NewStore`, `Claim`.

- [ ] **Step 6: Write `internal/idempotency/store.go`**

```go
package idempotency

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema is the idempotency_keys DDL. Each service that accepts an
// Idempotency-Key copies it into its own migrations.
const Schema = `
CREATE TABLE idempotency_keys (
    key           text PRIMARY KEY,
    request_hash  text        NOT NULL,
    state         text        NOT NULL,
    resource_type text        NOT NULL DEFAULT '',
    resource_id   text        NOT NULL DEFAULT '',
    response_body json,
    status_code   integer     NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT idempotency_keys_state_valid
        CHECK (state IN ('in_progress', 'completed'))
);
`

// Key states.
const (
	StateInProgress = "in_progress"
	StateCompleted  = "completed"
)

// Outcome is what a Claim decided.
type Outcome int

// Claim outcomes.
const (
	// OutcomeOwned means this request holds the key and should run.
	OutcomeOwned Outcome = iota
	// OutcomeReplay means the key completed; return the stored response.
	OutcomeReplay
	// OutcomeInProgress means another request holds the key and has not finished.
	OutcomeInProgress
	// OutcomeConflict means the key was used with a different request body.
	OutcomeConflict
)

// Record is a stored idempotency key.
type Record struct {
	Key          string
	RequestHash  string
	State        string
	ResourceType string
	ResourceID   string
	ResponseBody []byte
	StatusCode   int
}

// Store reads and writes idempotency keys.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Claim attempts to take ownership of key. Exactly one concurrent caller
// receives OutcomeOwned; the insert-or-nothing is what elects the owner.
func (s *Store) Claim(ctx context.Context, key, requestHash string) (Outcome, Record, error) {
	const insert = `
INSERT INTO idempotency_keys (key, request_hash, state)
VALUES ($1, $2, 'in_progress')
ON CONFLICT (key) DO NOTHING
RETURNING key`

	var claimed string
	err := s.pool.QueryRow(ctx, insert, key, requestHash).Scan(&claimed)
	switch {
	case err == nil:
		return OutcomeOwned, Record{Key: key, RequestHash: requestHash, State: StateInProgress}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return 0, Record{}, fmt.Errorf("idempotency: claim: %w", err)
	}

	existing, err := s.get(ctx, key)
	if err != nil {
		return 0, Record{}, err
	}
	if existing.RequestHash != requestHash {
		return OutcomeConflict, existing, nil
	}
	if existing.State == StateCompleted {
		return OutcomeReplay, existing, nil
	}
	return OutcomeInProgress, existing, nil
}

func (s *Store) get(ctx context.Context, key string) (Record, error) {
	const query = `
SELECT key, request_hash, state, resource_type, resource_id,
       coalesce(response_body, 'null'::json), status_code
  FROM idempotency_keys
 WHERE key = $1`

	var r Record
	err := s.pool.QueryRow(ctx, query, key).Scan(
		&r.Key, &r.RequestHash, &r.State, &r.ResourceType,
		&r.ResourceID, &r.ResponseBody, &r.StatusCode,
	)
	if err != nil {
		return Record{}, fmt.Errorf("idempotency: get %s: %w", key, err)
	}
	return r, nil
}

// Release drops an in-progress claim so the request can be retried. It never
// removes a completed key.
func (s *Store) Release(ctx context.Context, key string) error {
	const query = `DELETE FROM idempotency_keys WHERE key = $1 AND state = 'in_progress'`

	if _, err := s.pool.Exec(ctx, query, key); err != nil {
		return fmt.Errorf("idempotency: release %s: %w", key, err)
	}
	return nil
}

// Completion is the result recorded against a claimed key.
type Completion struct {
	Key          string
	ResourceType string
	ResourceID   string
	StatusCode   int
	ResponseBody []byte
}

// Complete flips a claimed key to completed inside the caller's transaction,
// so the key and the resource it names commit together.
func Complete(ctx context.Context, tx pgx.Tx, c Completion) error {
	const query = `
UPDATE idempotency_keys
   SET state = 'completed', resource_type = $2, resource_id = $3,
       status_code = $4, response_body = $5
 WHERE key = $1 AND state = 'in_progress'`

	tag, err := tx.Exec(ctx, query, c.Key, c.ResourceType, c.ResourceID, c.StatusCode, c.ResponseBody)
	if err != nil {
		return fmt.Errorf("idempotency: complete %s: %w", c.Key, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("idempotency: complete %s: no in-progress claim", c.Key)
	}
	return nil
}
```

- [ ] **Step 7: Run the store tests to verify they pass**

Run: `go test -tags=integration -race ./internal/idempotency/... -v`
Expected: PASS, seven tests. `TestConcurrentClaimsElectOneOwner` is the one that
fails if the claim is written as SELECT-then-INSERT instead of
insert-or-nothing.

- [ ] **Step 8: Write the failing middleware tests**

`internal/idempotency/middleware_test.go`:

```go
//go:build integration

package idempotency_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

const requestBody = `{"customer_id":"cust_1","payment_method_id":"pm_ok"}`

// creator stands in for a handler that completes its idempotency claim inside
// its own transaction.
func creator(t *testing.T, pool *pgxpool.Pool, resourceID string) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, ok := idempotency.KeyFromContext(r.Context())
		if !ok {
			t.Error("handler ran without an idempotency key in context")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		received, err := io.ReadAll(r.Body)
		if err != nil || len(received) == 0 {
			t.Errorf("handler read %q from the request body, want the original payload", received)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		body := []byte(`{"order_id":"` + resourceID + `","status":"pending"}`)

		err := postgres.WithTx(r.Context(), pool, func(tx pgx.Tx) error {
			return idempotency.Complete(r.Context(), tx, idempotency.Completion{
				Key: key, ResourceType: "order", ResourceID: resourceID,
				StatusCode: http.StatusAccepted, ResponseBody: body,
			})
		})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(body)
	})
}

func post(t *testing.T, h http.Handler, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(body))
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMiddlewareRejectsMissingKey(t *testing.T) {
	_, pool, s := store(t)
	h := idempotency.Require(s)(creator(t, pool, "ord_1"))

	rec := post(t, h, "", requestBody)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "idempotency_key_required") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestMiddlewareReplaysIdenticalRequest(t *testing.T) {
	_, pool, s := store(t)
	h := idempotency.Require(s)(creator(t, pool, "ord_1"))

	first := post(t, h, "key-1", requestBody)
	second := post(t, h, "key-1", requestBody)

	if first.Code != http.StatusAccepted {
		t.Fatalf("first status = %d, want 202", first.Code)
	}
	if second.Code != first.Code {
		t.Errorf("replay status = %d, want %d", second.Code, first.Code)
	}
	if second.Body.String() != first.Body.String() {
		t.Errorf("replay body = %q, want %q", second.Body.String(), first.Body.String())
	}
}

func TestMiddlewareReplaysReorderedBody(t *testing.T) {
	_, pool, s := store(t)
	h := idempotency.Require(s)(creator(t, pool, "ord_1"))

	first := post(t, h, "key-1", requestBody)
	second := post(t, h, "key-1", `{"payment_method_id":"pm_ok","customer_id":"cust_1"}`)

	if second.Code != first.Code {
		t.Errorf("reordered body was treated as a conflict: status = %d", second.Code)
	}
}

func TestMiddlewareConflictsOnDifferentBody(t *testing.T) {
	_, pool, s := store(t)
	h := idempotency.Require(s)(creator(t, pool, "ord_1"))

	post(t, h, "key-1", requestBody)
	second := post(t, h, "key-1", `{"customer_id":"cust_2","payment_method_id":"pm_ok"}`)

	if second.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", second.Code)
	}
	if !strings.Contains(second.Body.String(), "idempotency_key_reused") {
		t.Errorf("body = %q", second.Body.String())
	}
}

func TestMiddlewareReportsInProgress(t *testing.T) {
	ctx, pool, s := store(t)
	if _, _, err := s.Claim(ctx, "key-1", mustHash(t, requestBody)); err != nil {
		t.Fatalf("pre-claim: %v", err)
	}

	h := idempotency.Require(s)(creator(t, pool, "ord_1"))
	rec := post(t, h, "key-1", requestBody)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "processing") {
		t.Errorf("body = %q, want the processing marker", rec.Body.String())
	}
}

func TestMiddlewareReleasesClaimOnHandlerFailure(t *testing.T) {
	_, _, s := store(t)
	failing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(r.Context(), w, http.StatusInternalServerError, "internal_error", "boom", nil)
	})
	h := idempotency.Require(s)(failing)

	if rec := post(t, h, "key-1", requestBody); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}

	outcome, _, err := s.Claim(context.Background(), "key-1", mustHash(t, requestBody))
	if err != nil {
		t.Fatalf("Claim after failure: %v", err)
	}
	if outcome != idempotency.OutcomeOwned {
		t.Fatalf("outcome = %v after a 500, want OutcomeOwned; the claim leaked", outcome)
	}
}

func TestMiddlewareReleasesClaimWhenHandlerPanics(t *testing.T) {
	_, _, s := store(t)
	panicking := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	})
	h := idempotency.Require(s)(panicking)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic did not propagate past the middleware")
			}
		}()
		post(t, h, "key-1", requestBody)
	}()

	outcome, _, err := s.Claim(context.Background(), "key-1", mustHash(t, requestBody))
	if err != nil {
		t.Fatalf("Claim after panic: %v", err)
	}
	if outcome != idempotency.OutcomeOwned {
		t.Fatalf("outcome = %v after a panic, want OutcomeOwned; the claim was stranded", outcome)
	}
}

func mustHash(t *testing.T, body string) string {
	t.Helper()
	h, err := idempotency.Hash([]byte(body))
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return h
}
```

Add `"github.com/jackc/pgx/v5/pgxpool"` to the imports.

- [ ] **Step 9: Write `internal/idempotency/middleware.go`**

```go
package idempotency

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
)

const headerName = "Idempotency-Key"

const maxBodyBytes = 1 << 20

type contextKey int

const keyContextKey contextKey = iota

// KeyFromContext returns the idempotency key this request holds.
func KeyFromContext(ctx context.Context) (string, bool) {
	key, ok := ctx.Value(keyContextKey).(string)
	return key, ok
}

type statusCapture struct {
	http.ResponseWriter
	status int
}

func (s *statusCapture) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusCapture) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Require enforces the Idempotency-Key header. It claims the key before the
// handler runs and releases the claim if the handler fails, so a retry is not
// permanently blocked by a transient error.
func Require(store *Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			key := r.Header.Get(headerName)
			if key == "" {
				httpx.WriteError(ctx, w, http.StatusBadRequest,
					"idempotency_key_required", "the Idempotency-Key header is required", nil)
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
			if err != nil {
				httpx.WriteError(ctx, w, http.StatusBadRequest,
					"invalid_request", "could not read the request body", nil)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			hash, err := Hash(body)
			if err != nil {
				httpx.WriteError(ctx, w, http.StatusBadRequest,
					"invalid_request", "the request body is not valid JSON", nil)
				return
			}

			outcome, record, err := store.Claim(ctx, key, hash)
			if err != nil {
				httpx.WriteError(ctx, w, http.StatusInternalServerError,
					"internal_error", "internal error", nil)
				return
			}

			switch outcome {
			case OutcomeConflict:
				httpx.WriteError(ctx, w, http.StatusConflict,
					"idempotency_key_reused",
					"this Idempotency-Key was used with a different request body", nil)
				return
			case OutcomeInProgress:
				httpx.WriteJSON(ctx, w, http.StatusAccepted,
					map[string]string{"status": "processing"})
				return
			case OutcomeReplay:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotent-Replay", "true")
				w.WriteHeader(record.StatusCode)
				_, _ = w.Write(record.ResponseBody)
				return
			}

			capture := &statusCapture{ResponseWriter: w}
			handlerReturned := false

			// Deferred so the claim is released on a panic as well as on a
			// non-2xx. A validation failure records no result, so the client
			// can correct the request and retry under the same key.
			defer func() {
				succeeded := handlerReturned &&
					capture.status >= http.StatusOK &&
					capture.status < http.StatusMultipleChoices
				if !succeeded {
					_ = store.Release(context.WithoutCancel(ctx), key)
				}
			}()

			next.ServeHTTP(capture, r.WithContext(context.WithValue(ctx, keyContextKey, key)))
			handlerReturned = true
		})
	}
}
```

The release uses `context.WithoutCancel` so a client that hangs up after a 500
still gets its claim released, rather than leaving the key stuck in progress.

- [ ] **Step 10: Run the middleware tests to verify they pass**

Run: `go test -tags=integration -race ./internal/idempotency/... -v`
Expected: PASS, seventeen tests: three hash, seven store, seven middleware.

- [ ] **Step 11: Commit**

```bash
git add internal/idempotency
git commit -m "feat(idempotency): claim, replay, conflict and in-transaction completion

The middleware claims the key with insert-or-nothing before the handler runs, so
exactly one concurrent request owns it; the use case flips the claim to
completed inside the same transaction as the resource, so the key and the order
it names commit together. A 5xx releases the claim rather than blocking retries."
```

---

### Task 11: order service vertical slice — migrations, store, use cases, handlers

**Closes:** PL-25, PL-27.

**Files:**
- Create: `services/order/migrations/00001_orders.sql`,
  `services/order/migrations/00002_infrastructure.sql`,
  `services/order/migrations/migrations.go`
- Create: `services/order/app/ports.go`, `services/order/app/create_order.go`,
  `services/order/app/get_order.go`, `services/order/app/create_order_test.go`
- Create: `services/order/adapter/pricing/static.go`,
  `services/order/adapter/pricing/static_test.go`
- Create: `services/order/adapter/postgres/store.go`,
  `services/order/adapter/postgres/store_test.go`
- Create: `services/order/adapter/http/handler.go`,
  `services/order/adapter/http/routes.go`,
  `services/order/adapter/http/handler_test.go`
- Modify: `cmd/order/main.go` — wire pool, migrations, store, use cases, routes,
  outbox publisher

**Interfaces:**
- Consumes: everything from Tasks 2 through 10.
- Produces:
  - `app.OrderStore`, `app.Pricer` (ports)
  - `app.CreateOrderCommand`, `app.CreateOrderResult`, `app.CreateOrder`
  - `app.GetOrder`
  - `app.ErrUnknownSKU`
  - `orderhttp.NewHandler`, `orderhttp.Register`

**Why a Pricer exists.** `orders.total_amount` has to be filled by something, and
there is no catalogue service in this lab. A `Pricer` port with a small static
implementation keeps the schema honest and gives Epic C a seam to hang real
pricing on, at the cost of about twenty lines. Adding a catalogue service would
be inventing scope.

- [ ] **Step 1: Write `services/order/migrations/00001_orders.sql`**

The CHECK constrains the status *domain*. It cannot constrain the legal *edges*,
because a CHECK cannot see the previous value. The transition trigger belongs in
Epic D, where transitions first happen.

```sql
-- +goose Up
CREATE TABLE orders (
    id                text        PRIMARY KEY,
    customer_id       text        NOT NULL,
    status            text        NOT NULL,
    total_amount      bigint      NOT NULL CHECK (total_amount >= 0),
    currency          text        NOT NULL,
    payment_method_id text        NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT orders_status_valid CHECK (status IN (
        'pending', 'inventory_reserved', 'payment_pending', 'payment_succeeded',
        'payment_failed', 'confirmed', 'cancelled', 'expired',
        'awaiting_reconciliation', 'manual_review'
    ))
);

CREATE TABLE order_items (
    order_id   text    NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    sku        text    NOT NULL,
    quantity   integer NOT NULL CHECK (quantity > 0),
    unit_price bigint  NOT NULL CHECK (unit_price >= 0),
    PRIMARY KEY (order_id, sku)
);

-- +goose Down
DROP TABLE order_items;
DROP TABLE orders;
```

- [ ] **Step 2: Write `services/order/migrations/00002_infrastructure.sql`**

Copy the DDL verbatim from `outbox.Schema` and `idempotency.Schema`, wrapped in
goose directives. The duplication across services is intended: four services own
four databases with four migration histories.

```sql
-- +goose Up
CREATE TABLE outbox_events (
    id              uuid        PRIMARY KEY,
    aggregate_type  text        NOT NULL,
    aggregate_id    text        NOT NULL,
    event_type      text        NOT NULL,
    payload         jsonb       NOT NULL,
    status          text        NOT NULL DEFAULT 'pending',
    retry_count     integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    published_at    timestamptz,
    last_error      text,
    CONSTRAINT outbox_events_status_valid
        CHECK (status IN ('pending', 'published', 'retrying', 'dead_lettered'))
);

CREATE INDEX outbox_events_claimable
    ON outbox_events (next_attempt_at)
    WHERE status IN ('pending', 'retrying');

-- response_body is json rather than jsonb: jsonb canonicalises on storage,
-- reordering object keys, and a replay must return the original bytes.
CREATE TABLE idempotency_keys (
    key           text        PRIMARY KEY,
    request_hash  text        NOT NULL,
    state         text        NOT NULL,
    resource_type text        NOT NULL DEFAULT '',
    resource_id   text        NOT NULL DEFAULT '',
    response_body json,
    status_code   integer     NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT idempotency_keys_state_valid
        CHECK (state IN ('in_progress', 'completed'))
);

-- +goose Down
DROP TABLE idempotency_keys;
DROP INDEX outbox_events_claimable;
DROP TABLE outbox_events;
```

- [ ] **Step 3: Write `services/order/migrations/migrations.go`**

```go
// Package migrations embeds the order service schema.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS returns the embedded migration files.
func FS() fs.FS {
	return files
}
```

- [ ] **Step 4: Write the pricing adapter and its test**

`services/order/adapter/pricing/static_test.go`:

```go
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
```

`services/order/adapter/pricing/static.go`:

```go
// Package pricing provides unit prices for the order service.
package pricing

import (
	"context"
	"fmt"

	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
)

// Static prices a fixed catalogue. Epic C replaces it if real pricing is needed.
type Static struct {
	prices map[string]int64
}

// NewStatic returns the lab catalogue. Prices are in minor currency units.
func NewStatic() *Static {
	return &Static{prices: map[string]int64{
		"playstation-5": 150000,
		"xbox-series-x": 140000,
		"switch-2":      95000,
	}}
}

// UnitPrice returns the price of one unit of sku.
func (s *Static) UnitPrice(_ context.Context, sku string) (int64, error) {
	price, ok := s.prices[sku]
	if !ok {
		return 0, fmt.Errorf("%w: %s", app.ErrUnknownSKU, sku)
	}
	return price, nil
}

// Currency returns the currency every price is quoted in.
func (s *Static) Currency() string {
	return "NGN"
}
```

- [ ] **Step 5: Write `services/order/app/ports.go`**

```go
// Package app holds the order service use cases. Port interfaces are declared
// here, on the consumer side, and implemented under adapter/.
package app

import (
	"context"
	"errors"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

// ErrUnknownSKU is returned when no price exists for a requested sku.
var ErrUnknownSKU = errors.New("unknown sku")

// OrderStore persists orders.
type OrderStore interface {
	// CreateOrder writes the order, its items, the outbox events and, when
	// completion is not nil, the idempotency completion, in one transaction.
	// Either all of them exist afterwards or none of them do.
	CreateOrder(ctx context.Context, order domain.Order, events []outbox.Event, completion *idempotency.Completion) error

	// GetOrder returns the order, or domain.ErrOrderNotFound.
	GetOrder(ctx context.Context, id string) (domain.Order, error)
}

// Pricer supplies unit prices.
type Pricer interface {
	UnitPrice(ctx context.Context, sku string) (int64, error)
	Currency() string
}
```

- [ ] **Step 6: Write the failing use case tests**

`services/order/app/create_order_test.go`:

```go
package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

type fakeStore struct {
	order      domain.Order
	events     []outbox.Event
	completion *idempotency.Completion
	err        error
	calls      int
}

func (f *fakeStore) CreateOrder(_ context.Context, o domain.Order, e []outbox.Event, c *idempotency.Completion) error {
	f.calls++
	if f.err != nil {
		return f.err
	}
	f.order, f.events, f.completion = o, e, c
	return nil
}

func (f *fakeStore) GetOrder(_ context.Context, id string) (domain.Order, error) {
	if f.order.ID != id {
		return domain.Order{}, domain.ErrOrderNotFound
	}
	return f.order, nil
}

type fakePricer struct{ err error }

func (f fakePricer) UnitPrice(_ context.Context, sku string) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if sku == "playstation-5" {
		return 150000, nil
	}
	return 50000, nil
}

func (f fakePricer) Currency() string { return "NGN" }

func newUseCase(store app.OrderStore) *app.CreateOrder {
	return app.NewCreateOrder(store, fakePricer{}, func() string { return "ord_test" })
}

func validCommand() app.CreateOrderCommand {
	return app.CreateOrderCommand{
		CustomerID:      "cust_1",
		PaymentMethodID: "pm_ok",
		Items:           []domain.Item{{SKU: "playstation-5", Quantity: 2}},
	}
}

func TestCreateOrderPersistsPendingOrderWithPricedItems(t *testing.T) {
	store := &fakeStore{}

	result, err := newUseCase(store).Execute(context.Background(), validCommand(), "key-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if result.OrderID != "ord_test" || result.Status != string(domain.StatusPending) {
		t.Fatalf("result = %+v", result)
	}
	if store.order.TotalAmount != 300000 {
		t.Errorf("TotalAmount = %d, want 300000", store.order.TotalAmount)
	}
	if store.order.Currency != "NGN" {
		t.Errorf("Currency = %q, want NGN", store.order.Currency)
	}
	if store.order.Items[0].UnitPrice != 150000 {
		t.Errorf("UnitPrice = %d, want 150000", store.order.Items[0].UnitPrice)
	}
}

func TestCreateOrderEmitsOrderCreated(t *testing.T) {
	store := &fakeStore{}

	if _, err := newUseCase(store).Execute(context.Background(), validCommand(), "key-1"); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(store.events) != 1 {
		t.Fatalf("emitted %d events, want 1", len(store.events))
	}
	e := store.events[0]
	if e.EventType != "OrderCreated" {
		t.Errorf("EventType = %q", e.EventType)
	}
	if e.AggregateType != "order" || e.AggregateID != "ord_test" {
		t.Errorf("aggregate = %s/%s", e.AggregateType, e.AggregateID)
	}
	if e.ID.String() == "" {
		t.Error("event has no id")
	}
}

func TestCreateOrderPassesIdempotencyCompletion(t *testing.T) {
	store := &fakeStore{}

	result, err := newUseCase(store).Execute(context.Background(), validCommand(), "key-1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if store.completion == nil {
		t.Fatal("no completion passed to the store")
	}
	if store.completion.Key != "key-1" || store.completion.ResourceID != "ord_test" {
		t.Errorf("completion = %+v", store.completion)
	}
	if store.completion.StatusCode != http.StatusAccepted {
		t.Errorf("StatusCode = %d, want 202", store.completion.StatusCode)
	}

	var replayed app.CreateOrderResult
	if err := json.Unmarshal(store.completion.ResponseBody, &replayed); err != nil {
		t.Fatalf("stored body is not the result: %v", err)
	}
	if replayed != result {
		t.Errorf("stored body %+v does not match the returned result %+v", replayed, result)
	}
}

func TestCreateOrderWithoutKeyPassesNoCompletion(t *testing.T) {
	store := &fakeStore{}

	if _, err := newUseCase(store).Execute(context.Background(), validCommand(), ""); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if store.completion != nil {
		t.Errorf("completion = %+v, want nil", store.completion)
	}
}

func TestCreateOrderRejectsInvalidCommandBeforeTouchingTheStore(t *testing.T) {
	cases := []struct {
		name string
		cmd  app.CreateOrderCommand
		want error
	}{
		{"no customer", app.CreateOrderCommand{PaymentMethodID: "pm_ok",
			Items: []domain.Item{{SKU: "playstation-5", Quantity: 1}}}, domain.ErrMissingCustomer},
		{"no payment method", app.CreateOrderCommand{CustomerID: "cust_1",
			Items: []domain.Item{{SKU: "playstation-5", Quantity: 1}}}, domain.ErrMissingPaymentMethod},
		{"no items", app.CreateOrderCommand{CustomerID: "cust_1",
			PaymentMethodID: "pm_ok"}, domain.ErrNoItems},
		{"zero quantity", app.CreateOrderCommand{CustomerID: "cust_1", PaymentMethodID: "pm_ok",
			Items: []domain.Item{{SKU: "playstation-5", Quantity: 0}}}, domain.ErrInvalidQuantity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			_, err := newUseCase(store).Execute(context.Background(), tc.cmd, "key-1")
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if store.calls != 0 {
				t.Error("the store was called for an invalid command")
			}
		})
	}
}

func TestCreateOrderSurfacesUnknownSKU(t *testing.T) {
	store := &fakeStore{}
	uc := app.NewCreateOrder(store, fakePricer{err: app.ErrUnknownSKU}, func() string { return "ord_test" })

	_, err := uc.Execute(context.Background(), validCommand(), "key-1")
	if !errors.Is(err, app.ErrUnknownSKU) {
		t.Fatalf("err = %v, want ErrUnknownSKU", err)
	}
	if store.calls != 0 {
		t.Error("the store was called after a pricing failure")
	}
}
```

- [ ] **Step 7: Run to verify failure**

Run: `go test ./services/order/app/...`
Expected: FAIL — undefined `app.NewCreateOrder`.

- [ ] **Step 8: Write `services/order/app/create_order.go`**

```go
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

// CreateOrderCommand is a request to create an order. Item unit prices are
// filled in by the Pricer; callers supply sku and quantity only.
type CreateOrderCommand struct {
	CustomerID      string
	PaymentMethodID string
	Items           []domain.Item
}

// CreateOrderResult is the response body for a created order. It is also what
// is stored against the idempotency key and replayed on a duplicate request.
type CreateOrderResult struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
}

// OrderCreatedPayload is the OrderCreated event body.
type OrderCreatedPayload struct {
	OrderID         string        `json:"order_id"`
	CustomerID      string        `json:"customer_id"`
	PaymentMethodID string        `json:"payment_method_id"`
	TotalAmount     int64         `json:"total_amount"`
	Currency        string        `json:"currency"`
	Items           []domain.Item `json:"items"`
}

// CreateOrder creates an order in the pending state.
type CreateOrder struct {
	store  OrderStore
	pricer Pricer
	newID  func() string
}

// NewCreateOrder builds the use case.
func NewCreateOrder(store OrderStore, pricer Pricer, newID func() string) *CreateOrder {
	return &CreateOrder{store: store, pricer: pricer, newID: newID}
}

// Execute validates, prices and persists the order together with its
// OrderCreated event and, when idempotencyKey is not empty, the completion of
// that key. The saga advances the order asynchronously from Epic D onwards.
func (uc *CreateOrder) Execute(ctx context.Context, cmd CreateOrderCommand, idempotencyKey string) (CreateOrderResult, error) {
	if err := domain.Validate(cmd.CustomerID, cmd.PaymentMethodID, cmd.Items); err != nil {
		return CreateOrderResult{}, err
	}

	items := make([]domain.Item, 0, len(cmd.Items))
	for _, requested := range cmd.Items {
		price, err := uc.pricer.UnitPrice(ctx, requested.SKU)
		if err != nil {
			return CreateOrderResult{}, err
		}
		items = append(items, domain.Item{
			SKU:       requested.SKU,
			Quantity:  requested.Quantity,
			UnitPrice: price,
		})
	}

	order := domain.Order{
		ID:              uc.newID(),
		CustomerID:      cmd.CustomerID,
		Status:          domain.StatusPending,
		TotalAmount:     domain.Total(items),
		Currency:        uc.pricer.Currency(),
		PaymentMethodID: cmd.PaymentMethodID,
		Items:           items,
	}

	result := CreateOrderResult{OrderID: order.ID, Status: string(order.Status)}

	event, err := outbox.NewEvent("order", order.ID, "OrderCreated", OrderCreatedPayload{
		OrderID:         order.ID,
		CustomerID:      order.CustomerID,
		PaymentMethodID: order.PaymentMethodID,
		TotalAmount:     order.TotalAmount,
		Currency:        order.Currency,
		Items:           order.Items,
	})
	if err != nil {
		return CreateOrderResult{}, err
	}

	var completion *idempotency.Completion
	if idempotencyKey != "" {
		body, err := json.Marshal(result)
		if err != nil {
			return CreateOrderResult{}, fmt.Errorf("app: marshal create order result: %w", err)
		}
		completion = &idempotency.Completion{
			Key:          idempotencyKey,
			ResourceType: "order",
			ResourceID:   order.ID,
			StatusCode:   http.StatusAccepted,
			ResponseBody: body,
		}
	}

	if err := uc.store.CreateOrder(ctx, order, []outbox.Event{event}, completion); err != nil {
		return CreateOrderResult{}, err
	}
	return result, nil
}
```

- [ ] **Step 9: Write `services/order/app/get_order.go`**

```go
package app

import (
	"context"

	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

// GetOrder reads a single order.
type GetOrder struct {
	store OrderStore
}

// NewGetOrder builds the use case.
func NewGetOrder(store OrderStore) *GetOrder {
	return &GetOrder{store: store}
}

// Execute returns the order, or domain.ErrOrderNotFound.
func (uc *GetOrder) Execute(ctx context.Context, id string) (domain.Order, error) {
	return uc.store.GetOrder(ctx, id)
}
```

- [ ] **Step 10: Run the use case tests to verify they pass**

Run: `go test ./services/order/... -race -v`
Expected: PASS.

- [ ] **Step 11: Write `services/order/adapter/postgres/store.go`**

```go
// Package postgres implements the order service ports against Postgres.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	infrapg "github.com/tunedev/go-resilient-commerce-lab/internal/postgres" // aliased: this package is also named postgres
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

// Store is the OrderStore implementation.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// CreateOrder writes the order, its items, the outbox events and the
// idempotency completion in one transaction.
func (s *Store) CreateOrder(ctx context.Context, order domain.Order, events []outbox.Event, completion *idempotency.Completion) error {
	return infrapg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		const insertOrder = `
INSERT INTO orders (id, customer_id, status, total_amount, currency, payment_method_id)
VALUES ($1, $2, $3, $4, $5, $6)`

		_, err := tx.Exec(ctx, insertOrder, order.ID, order.CustomerID, order.Status,
			order.TotalAmount, order.Currency, order.PaymentMethodID)
		if err != nil {
			return fmt.Errorf("order: insert order: %w", err)
		}

		const insertItem = `
INSERT INTO order_items (order_id, sku, quantity, unit_price) VALUES ($1, $2, $3, $4)`

		for _, item := range order.Items {
			if _, err := tx.Exec(ctx, insertItem, order.ID, item.SKU, item.Quantity, item.UnitPrice); err != nil {
				return fmt.Errorf("order: insert item %s: %w", item.SKU, err)
			}
		}

		if err := outbox.Append(ctx, tx, events...); err != nil {
			return err
		}

		if completion != nil {
			if err := idempotency.Complete(ctx, tx, *completion); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetOrder returns the order with its items.
func (s *Store) GetOrder(ctx context.Context, id string) (domain.Order, error) {
	const selectOrder = `
SELECT id, customer_id, status, total_amount, currency, payment_method_id, created_at, updated_at
  FROM orders WHERE id = $1`

	var o domain.Order
	err := s.pool.QueryRow(ctx, selectOrder, id).Scan(
		&o.ID, &o.CustomerID, &o.Status, &o.TotalAmount,
		&o.Currency, &o.PaymentMethodID, &o.CreatedAt, &o.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Order{}, domain.ErrOrderNotFound
	}
	if err != nil {
		return domain.Order{}, fmt.Errorf("order: select order %s: %w", id, err)
	}

	const selectItems = `SELECT sku, quantity, unit_price FROM order_items WHERE order_id = $1 ORDER BY sku`

	rows, err := s.pool.Query(ctx, selectItems, id)
	if err != nil {
		return domain.Order{}, fmt.Errorf("order: select items %s: %w", id, err)
	}
	defer rows.Close()

	for rows.Next() {
		var item domain.Item
		if err := rows.Scan(&item.SKU, &item.Quantity, &item.UnitPrice); err != nil {
			return domain.Order{}, fmt.Errorf("order: scan item: %w", err)
		}
		o.Items = append(o.Items, item)
	}
	if err := rows.Err(); err != nil {
		return domain.Order{}, fmt.Errorf("order: item rows: %w", err)
	}
	return o, nil
}
```

- [ ] **Step 12: Write the store integration test**

`services/order/adapter/postgres/store_test.go`:

```go
//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	infrapg "github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
	orderpg "github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/postgres"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/migrations"
)

func newStore(t *testing.T) (context.Context, *pgxpool.Pool, *orderpg.Store) {
	t.Helper()
	ctx := context.Background()
	pool := infrapg.StartPostgres(t)
	if err := infrapg.Migrate(ctx, pool, migrations.FS()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return ctx, pool, orderpg.NewStore(pool)
}

func sampleOrder() domain.Order {
	return domain.Order{
		ID: "ord_1", CustomerID: "cust_1", Status: domain.StatusPending,
		TotalAmount: 150000, Currency: "NGN", PaymentMethodID: "pm_ok",
		Items: []domain.Item{{SKU: "playstation-5", Quantity: 1, UnitPrice: 150000}},
	}
}

func TestCreateOrderWritesOrderItemsOutboxAndCompletionAtomically(t *testing.T) {
	ctx, pool, store := newStore(t)

	if _, _, err := idempotency.NewStore(pool).Claim(ctx, "key-1", "hash-a"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	event, err := outbox.NewEvent("order", "ord_1", "OrderCreated", map[string]string{"order_id": "ord_1"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	err = store.CreateOrder(ctx, sampleOrder(), []outbox.Event{event}, &idempotency.Completion{
		Key: "key-1", ResourceType: "order", ResourceID: "ord_1",
		StatusCode: http.StatusAccepted, ResponseBody: []byte(`{"order_id":"ord_1"}`),
	})
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	assertCount(t, ctx, pool, `SELECT count(*) FROM orders`, 1)
	assertCount(t, ctx, pool, `SELECT count(*) FROM order_items`, 1)
	assertCount(t, ctx, pool, `SELECT count(*) FROM outbox_events`, 1)
	assertCount(t, ctx, pool, `SELECT count(*) FROM idempotency_keys WHERE state = 'completed'`, 1)
}

func TestCreateOrderRollsBackEverythingWhenCompletionFails(t *testing.T) {
	ctx, pool, store := newStore(t)

	event, err := outbox.NewEvent("order", "ord_1", "OrderCreated", map[string]string{"order_id": "ord_1"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	// The key was never claimed, so Complete fails and must take the order with it.
	err = store.CreateOrder(ctx, sampleOrder(), []outbox.Event{event}, &idempotency.Completion{
		Key: "never-claimed", ResourceType: "order", ResourceID: "ord_1",
		StatusCode: http.StatusAccepted, ResponseBody: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("CreateOrder succeeded with an unclaimed key")
	}

	assertCount(t, ctx, pool, `SELECT count(*) FROM orders`, 0)
	assertCount(t, ctx, pool, `SELECT count(*) FROM order_items`, 0)
	assertCount(t, ctx, pool, `SELECT count(*) FROM outbox_events`, 0)
}

func TestGetOrderReturnsItems(t *testing.T) {
	ctx, _, store := newStore(t)

	if err := store.CreateOrder(ctx, sampleOrder(), nil, nil); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}

	got, err := store.GetOrder(ctx, "ord_1")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].SKU != "playstation-5" {
		t.Errorf("Items = %+v", got.Items)
	}
	if got.Status != domain.StatusPending {
		t.Errorf("Status = %q", got.Status)
	}
}

func TestGetOrderNotFound(t *testing.T) {
	ctx, _, store := newStore(t)

	_, err := store.GetOrder(ctx, "ord_missing")
	if !errors.Is(err, domain.ErrOrderNotFound) {
		t.Fatalf("err = %v, want ErrOrderNotFound", err)
	}
}

func assertCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, query string, want int) {
	t.Helper()
	var got int
	if err := pool.QueryRow(ctx, query).Scan(&got); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if got != want {
		t.Errorf("%s = %d, want %d", query, got, want)
	}
}
```

Add `"github.com/jackc/pgx/v5/pgxpool"` to the imports.

`TestCreateOrderRollsBackEverythingWhenCompletionFails` is the test that proves
the order and its key cannot diverge.

- [ ] **Step 13: Write the HTTP adapter**

`services/order/adapter/http/handler.go`:

```go
// Package http adapts the order use cases to HTTP.
package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

// Handler serves the order endpoints.
type Handler struct {
	create *app.CreateOrder
	get    *app.GetOrder
}

// NewHandler builds the handler.
func NewHandler(create *app.CreateOrder, get *app.GetOrder) *Handler {
	return &Handler{create: create, get: get}
}

type createRequest struct {
	CustomerID      string `json:"customer_id"`
	PaymentMethodID string `json:"payment_method_id"`
	Items           []struct {
		SKU      string `json:"sku"`
		Quantity int    `json:"quantity"`
	} `json:"items"`
}

type itemResponse struct {
	SKU       string `json:"sku"`
	Quantity  int    `json:"quantity"`
	UnitPrice int64  `json:"unit_price"`
}

type orderResponse struct {
	OrderID         string         `json:"order_id"`
	CustomerID      string         `json:"customer_id"`
	Status          string         `json:"status"`
	TotalAmount     int64          `json:"total_amount"`
	Currency        string         `json:"currency"`
	PaymentMethodID string         `json:"payment_method_id"`
	Items           []itemResponse `json:"items"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
}

// Create handles POST /orders.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req createRequest
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		httpx.WriteError(ctx, w, http.StatusBadRequest,
			"invalid_request", "the request body is not valid JSON", nil)
		return
	}

	items := make([]domain.Item, 0, len(req.Items))
	for _, it := range req.Items {
		items = append(items, domain.Item{SKU: it.SKU, Quantity: it.Quantity})
	}

	key, _ := idempotency.KeyFromContext(ctx)

	result, err := h.create.Execute(ctx, app.CreateOrderCommand{
		CustomerID:      req.CustomerID,
		PaymentMethodID: req.PaymentMethodID,
		Items:           items,
	}, key)
	if err != nil {
		writeDomainError(ctx, w, err)
		return
	}

	httpx.WriteJSON(ctx, w, http.StatusAccepted, result)
}

// Get handles GET /orders/{id}.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	order, err := h.get.Execute(ctx, r.PathValue("id"))
	if err != nil {
		writeDomainError(ctx, w, err)
		return
	}

	items := make([]itemResponse, 0, len(order.Items))
	for _, it := range order.Items {
		items = append(items, itemResponse{SKU: it.SKU, Quantity: it.Quantity, UnitPrice: it.UnitPrice})
	}

	httpx.WriteJSON(ctx, w, http.StatusOK, orderResponse{
		OrderID:         order.ID,
		CustomerID:      order.CustomerID,
		Status:          string(order.Status),
		TotalAmount:     order.TotalAmount,
		Currency:        order.Currency,
		PaymentMethodID: order.PaymentMethodID,
		Items:           items,
		CreatedAt:       order.CreatedAt,
		UpdatedAt:       order.UpdatedAt,
	})
}

func writeDomainError(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrOrderNotFound):
		httpx.WriteError(ctx, w, http.StatusNotFound, "order_not_found", "order not found", nil)
	case errors.Is(err, app.ErrUnknownSKU):
		httpx.WriteError(ctx, w, http.StatusUnprocessableEntity, "unknown_sku", err.Error(), nil)
	case errors.Is(err, domain.ErrMissingCustomer),
		errors.Is(err, domain.ErrMissingPaymentMethod),
		errors.Is(err, domain.ErrNoItems),
		errors.Is(err, domain.ErrInvalidQuantity):
		httpx.WriteError(ctx, w, http.StatusBadRequest, "invalid_request", err.Error(), nil)
	default:
		httpx.WriteError(ctx, w, http.StatusInternalServerError, "internal_error", "internal error", nil)
	}
}
```

Add `"context"` to the import block.

`services/order/adapter/http/routes.go`:

```go
package http

import (
	"net/http"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
)

// Register wires the order routes. Only POST /orders requires an
// Idempotency-Key; a read is already safe to repeat.
func Register(mux *http.ServeMux, h *Handler, store *idempotency.Store) {
	httpx.Route(mux, "POST /orders", idempotency.Require(store)(http.HandlerFunc(h.Create)))
	httpx.Route(mux, "GET /orders/{id}", http.HandlerFunc(h.Get))
}
```

- [ ] **Step 14: Write the handler tests**

The idempotency middleware is not in this chain; it is covered in Task 10. The
GET route is registered on a real `http.ServeMux` so that `r.PathValue("id")`
resolves — calling the handler function directly leaves the path value empty and
the test passes for the wrong reason.

`services/order/adapter/http/handler_test.go`:

```go
package http_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	orderhttp "github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/http"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/domain"
)

type stubStore struct {
	created domain.Order
	getErr  error
}

func (s *stubStore) CreateOrder(_ context.Context, o domain.Order, _ []outbox.Event, _ *idempotency.Completion) error {
	s.created = o
	return nil
}

func (s *stubStore) GetOrder(_ context.Context, id string) (domain.Order, error) {
	if s.getErr != nil {
		return domain.Order{}, s.getErr
	}
	return domain.Order{
		ID: id, CustomerID: "cust_1", Status: domain.StatusPending,
		TotalAmount: 150000, Currency: "NGN", PaymentMethodID: "pm_ok",
		Items: []domain.Item{{SKU: "playstation-5", Quantity: 1, UnitPrice: 150000}},
	}, nil
}

type stubPricer struct{ err error }

func (p stubPricer) UnitPrice(context.Context, string) (int64, error) {
	if p.err != nil {
		return 0, p.err
	}
	return 150000, nil
}

func (p stubPricer) Currency() string { return "NGN" }

func newMux(store app.OrderStore, pricer app.Pricer) *http.ServeMux {
	handler := orderhttp.NewHandler(
		app.NewCreateOrder(store, pricer, func() string { return "ord_test" }),
		app.NewGetOrder(store),
	)
	mux := http.NewServeMux()
	mux.Handle("POST /orders", http.HandlerFunc(handler.Create))
	mux.Handle("GET /orders/{id}", http.HandlerFunc(handler.Get))
	return mux
}

func do(mux *http.ServeMux, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

const validBody = `{"customer_id":"cust_1","payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}`

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return body.Error.Code
}

func TestCreateAccepted(t *testing.T) {
	store := &stubStore{}
	rec := do(newMux(store, stubPricer{}), http.MethodPost, "/orders", validBody)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", rec.Code, rec.Body.String())
	}

	var got app.CreateOrderResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OrderID != "ord_test" || got.Status != string(domain.StatusPending) {
		t.Errorf("body = %+v", got)
	}
	if store.created.TotalAmount != 150000 {
		t.Errorf("persisted total = %d, want 150000", store.created.TotalAmount)
	}
}

func TestCreateRejectsMalformedJSON(t *testing.T) {
	rec := do(newMux(&stubStore{}, stubPricer{}), http.MethodPost, "/orders", `{"customer_id":`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if code := errorCode(t, rec); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}
}

func TestCreateRejectsUnknownField(t *testing.T) {
	body := `{"customer_id":"cust_1","payment_method_id":"pm_ok","items":[],"discount":"none"}`
	rec := do(newMux(&stubStore{}, stubPricer{}), http.MethodPost, "/orders", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; the decoder must disallow unknown fields", rec.Code)
	}
}

func TestCreateRejectsMissingCustomer(t *testing.T) {
	body := `{"payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}`
	rec := do(newMux(&stubStore{}, stubPricer{}), http.MethodPost, "/orders", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if code := errorCode(t, rec); code != "invalid_request" {
		t.Errorf("code = %q, want invalid_request", code)
	}
}

func TestCreateRejectsUnknownSKU(t *testing.T) {
	mux := newMux(&stubStore{}, stubPricer{err: app.ErrUnknownSKU})
	rec := do(mux, http.MethodPost, "/orders", validBody)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if code := errorCode(t, rec); code != "unknown_sku" {
		t.Errorf("code = %q, want unknown_sku", code)
	}
}

func TestGetReturnsOrderWithItems(t *testing.T) {
	rec := do(newMux(&stubStore{}, stubPricer{}), http.MethodGet, "/orders/ord_test", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var got struct {
		OrderID string `json:"order_id"`
		Status  string `json:"status"`
		Items   []struct {
			SKU       string `json:"sku"`
			UnitPrice int64  `json:"unit_price"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.OrderID != "ord_test" {
		t.Errorf("order_id = %q; PathValue did not resolve", got.OrderID)
	}
	if len(got.Items) != 1 || got.Items[0].SKU != "playstation-5" {
		t.Errorf("items = %+v", got.Items)
	}
}

func TestGetNotFound(t *testing.T) {
	mux := newMux(&stubStore{getErr: domain.ErrOrderNotFound}, stubPricer{})
	rec := do(mux, http.MethodGet, "/orders/ord_missing", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if code := errorCode(t, rec); code != "order_not_found" {
		t.Errorf("code = %q, want order_not_found", code)
	}
}
```

- [ ] **Step 15: Wire it all in `cmd/order/main.go`**

The import block becomes:

```go
import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/tunedev/go-resilient-commerce-lab/internal/config"
	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/idempotency"
	"github.com/tunedev/go-resilient-commerce-lab/internal/logger"
	"github.com/tunedev/go-resilient-commerce-lab/internal/otelx"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
	orderhttp "github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/http"
	orderpg "github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/postgres"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/adapter/pricing"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/order/migrations"
)
```

Replace the body of `run` after the telemetry block with:

```go
	pool, err := postgres.NewPool(ctx, cfg.DatabaseDSN)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := postgres.Migrate(ctx, pool, migrations.FS()); err != nil {
		return err
	}

	store := orderpg.NewStore(pool)
	idemStore := idempotency.NewStore(pool)
	handler := orderhttp.NewHandler(
		app.NewCreateOrder(store, pricing.NewStatic(), func() string { return "ord_" + uuid.NewString() }),
		app.NewGetOrder(store),
	)

	mux := http.NewServeMux()
	httpx.Route(mux, "GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	orderhttp.Register(mux, handler, idemStore)

	publisher := outbox.NewPublisher(pool, outbox.LogSink{Logger: log}, log, outbox.PublisherOptions{
		Interval:    cfg.OutboxPollInterval,
		BatchSize:   50,
		MaxRetries:  5,
		BaseBackoff: time.Second,
	})

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return publisher.Run(groupCtx) })
	group.Go(func() error {
		return httpx.NewServer(httpx.Options{
			Addr:            cfg.HTTPAddr,
			Handler:         mux,
			Logger:          log,
			ShutdownTimeout: cfg.ShutdownTimeout,
		}).Run(groupCtx)
	})
	return group.Wait()
```

Add `golang.org/x/sync/errgroup`:

```bash
go get golang.org/x/sync/errgroup
```

- [ ] **Step 16: Run everything**

```bash
make lint && make test-race && make integration
make docker-up
```

Then exercise it by hand:

```bash
curl -is -X POST localhost:8080/orders \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: key-1' \
  -d '{"customer_id":"cust_1","payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}'
```

Expected: `202`, body `{"order_id":"ord_...","status":"pending"}`.

Repeat the identical command. Expected: the same `202` and the same body, plus
an `Idempotent-Replay: true` header.

Send it again with `"customer_id":"cust_2"` and the same key. Expected: `409`
with code `idempotency_key_reused`.

```bash
curl -s localhost:8080/orders/<the order id> | python3 -m json.tool
```

Expected: `200` with the items array and `"status": "pending"`.

Check the outbox drained: `docker compose -f deploy/docker-compose.yml logs order | grep "outbox event published"` shows one `OrderCreated` line.

Check Jaeger: the `POST /orders` trace is named after the route pattern, not the
concrete path.

- [ ] **Step 17: Commit**

```bash
make docker-down
git add services cmd/order go.mod go.sum
git commit -m "feat(order): POST /orders and GET /orders/{id} with transactional outbox

The order, its items, the OrderCreated event and the idempotency completion all
commit in one transaction, so a duplicate POST returns the first order rather
than creating a second, and no order can exist without the key that names it."
```

---

### Task 12: /readyz and /metrics

**Closes:** PL-28.

**Files:**
- Modify: `cmd/order/main.go` — register `/readyz` and `/metrics`
- Modify: `deploy/prometheus.yml` — already targets `order:8080`
- Modify: `README.md` — the liveness versus readiness note (written in Task 15)
- Create: `internal/httpx/health.go`, `internal/httpx/health_test.go`

**Interfaces:**
- Consumes: `otelx.Providers.Registry`, `*pgxpool.Pool`.
- Produces:
  - `func Health() http.Handler` — liveness, no dependency checks
  - `func Ready(check func(context.Context) error) http.Handler` — readiness
  - `func Metrics(registry *prometheus.Registry) http.Handler`

RED metrics need no new code: `otelhttp` already records
`http.server.request.duration` against the global meter provider that `otelx`
installed, and the Prometheus exporter surfaces them on the registry.

**Correction to story PL-28's wording:** the story asks `/readyz` to return 503
while migrations run. In this composition root migrations complete before the
server starts listening, so that window is not observable from outside and the
requirement is vacuous. `/readyz` checks the pool. If Epic H later needs a
service that serves before it is ready, move `Migrate` into a goroutine and gate
readiness on a flag then, when there is something to observe.

- [ ] **Step 1: Write the failing tests**

`internal/httpx/health_test.go`:

```go
package httpx_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
)

func TestHealthAlwaysOK(t *testing.T) {
	rec := httptest.NewRecorder()
	httpx.Health().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReadyReportsOKWhenTheCheckPasses(t *testing.T) {
	rec := httptest.NewRecorder()
	handler := httpx.Ready(func(context.Context) error { return nil })

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestReadyReports503WhenTheCheckFails(t *testing.T) {
	rec := httptest.NewRecorder()
	handler := httpx.Ready(func(context.Context) error { return errors.New("pool is down") })

	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
```

- [ ] **Step 2: Write `internal/httpx/health.go`**

```go
package httpx

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Health reports liveness. It checks nothing: a liveness probe that fails when
// a dependency is down turns a dependency outage into a restart loop.
func Health() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// Ready reports readiness. A failing check means the instance should be taken
// out of rotation, not restarted.
func Ready(check func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := check(r.Context()); err != nil {
			WriteError(r.Context(), w, http.StatusServiceUnavailable,
				"not_ready", "a dependency is unavailable", nil)
			return
		}
		WriteJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ready"})
	})
}

// Metrics serves the Prometheus exposition for registry.
func Metrics(registry *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
```

- [ ] **Step 3: Run the tests to verify they pass**

Run: `go test ./internal/httpx/... -race -v`
Expected: PASS, including the three added here.

- [ ] **Step 4: Register the routes in `cmd/order/main.go`**

Replace the inline `/healthz` closure with:

```go
	httpx.Route(mux, "GET /healthz", httpx.Health())
	httpx.Route(mux, "GET /readyz", httpx.Ready(pool.Ping))
	// Handle, not Route: scrapes should not open a span.
	mux.Handle("GET /metrics", httpx.Metrics(providers.Registry))
```

`/metrics` is registered with `mux.Handle` rather than `httpx.Route`: it should
not produce a trace span on every scrape.

- [ ] **Step 5: Verify against the running stack**

```bash
make docker-up
curl -s localhost:8080/readyz
curl -s localhost:8080/metrics | grep http_server_request_duration | head -3
```

Expected: `{"status":"ready"}`, and duration histogram lines.

Open `http://localhost:9090/targets`. Expected: the `order` target is **up**.
Issue a few `POST /orders` calls and confirm the request counter moves.

- [ ] **Step 6: Commit**

```bash
make docker-down
git add internal/httpx cmd/order
git commit -m "feat(httpx): liveness, readiness and metrics endpoints

Liveness checks nothing on purpose: a liveness probe that fails on a dependency
outage converts that outage into a restart loop. Readiness pings the pool.
/metrics is registered outside Route so scrapes do not produce spans."
```

---

### Task 13: labctl and the duplicate-order scenario

**Closes:** PL-30.

**Files:**
- Create: `cmd/labctl/main.go`, `cmd/labctl/client.go`,
  `cmd/labctl/scenario.go`, `cmd/labctl/duplicate_order.go`
- Modify: `.github/workflows/ci.yml` — add the `scenario` job, `main` only

All four files are `package main`. Introducing a package layer for one scenario
would be structure without a second case to justify it; extract when the
scenario count passes roughly six.

**Interfaces:**
- Consumes: the running stack over HTTP.
- Produces: `labctl scenario duplicate-order`, exit code 0 on pass and 1 on fail.

- [ ] **Step 1: Write `cmd/labctl/scenario.go`**

```go
package main

import (
	"context"
	"fmt"
	"sort"
)

// scenario is one reproducible demo. It asserts its own outcome.
type scenario struct {
	name        string
	description string
	run         func(ctx context.Context, env environment) error
}

type environment struct {
	orderBaseURL string
	jaegerURL    string
}

var scenarios = map[string]scenario{}

func register(s scenario) {
	scenarios[s.name] = s
}

func scenarioNames() []string {
	names := make([]string, 0, len(scenarios))
	for name := range scenarios {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func runScenario(ctx context.Context, name string, env environment) error {
	s, ok := scenarios[name]
	if !ok {
		return fmt.Errorf("unknown scenario %q; known scenarios: %v", name, scenarioNames())
	}
	fmt.Printf("running scenario %s: %s\n", s.name, s.description)
	return s.run(ctx, env)
}
```

- [ ] **Step 2: Write `cmd/labctl/client.go`**

```go
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
			return fmt.Errorf("build readiness request: %w", err)
		}
		if resp, err := c.do(req); err == nil && resp.Status == http.StatusOK {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", c.baseURL, ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s was not ready within %s", c.baseURL, timeout)
}
```

- [ ] **Step 3: Write `cmd/labctl/duplicate_order.go`**

```go
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
```

- [ ] **Step 4: Write `cmd/labctl/main.go`**

```go
// Command labctl drives the lab: it runs reproducible failure scenarios and
// injects infrastructure chaos.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	orderURL := flag.String("order-url", envOr("LABCTL_ORDER_URL", "http://localhost:8080"),
		"base URL of the order service")
	jaegerURL := flag.String("jaeger-url", envOr("LABCTL_JAEGER_URL", "http://localhost:16686"),
		"base URL of the Jaeger UI")
	flag.Parse()

	if flag.NArg() < 1 {
		usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	env := environment{orderBaseURL: *orderURL, jaegerURL: *jaegerURL}

	switch flag.Arg(0) {
	case "scenario":
		if flag.NArg() < 2 {
			fmt.Fprintf(os.Stderr, "scenario requires a name; known scenarios: %v\n", scenarioNames())
			os.Exit(2)
		}
		if err := runScenario(ctx, flag.Arg(1), env); err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("PASS\n")
		fmt.Printf("trace: %s/search?service=order\n", env.jaegerURL)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "usage: labctl [flags] scenario <name>\n\nknown scenarios:\n")
	for _, name := range scenarioNames() {
		fmt.Fprintf(os.Stderr, "  %s: %s\n", name, scenarios[name].description)
	}
	flag.PrintDefaults()
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 5: Run it against the live stack**

```bash
make docker-up
make scenario NAME=duplicate-order
```

Expected: `PASS`, the created order id, and a Jaeger search URL. Open that URL
and confirm three `POST /orders` spans exist and only one of them fans out to a
database insert.

Then break it deliberately and confirm the scenario catches it: comment out the
`OutcomeReplay` branch in `idempotency.Require`, rebuild, rerun. Expected:
`FAIL` and exit code 1. Restore the branch.

- [ ] **Step 6: Add the scenario job to CI**

```yaml
  scenario:
    if: github.ref == 'refs/heads/main'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: "1.27"
          cache: true
      - run: docker compose -f deploy/docker-compose.yml up -d --build
      - run: go run ./cmd/labctl scenario duplicate-order
      - if: failure()
        run: docker compose -f deploy/docker-compose.yml logs
      - if: always()
        run: docker compose -f deploy/docker-compose.yml down -v
```

- [ ] **Step 7: Commit**

```bash
make docker-down
git add cmd/labctl .github/workflows/ci.yml
git commit -m "feat(labctl): scenario runner and the duplicate-order demo

Each scenario asserts its own outcome and exits non-zero on failure, so the same
command that illustrates a blog post also gates CI."
```

---

### Task 14: Hugo site and the Pages workflow

**Closes:** PL-32.

**Files:**
- Create: `docs/hugo.toml`, `docs/layouts/shortcodes/diagram.html`,
  `docs/content/_index.md`, `docs/content/posts/_index.md`
- Create: `.github/workflows/pages.yml`
- Modify: `.gitignore` — already ignores `docs/public/` and `docs/resources/`

**Prerequisite, on the repository owner:** GitHub Pages must be enabled with
**Source: GitHub Actions**. The workflow cannot deploy otherwise.

**Nested module note.** `hugo mod init` writes a `go.mod` inside `docs/`. Go
tooling then treats `docs/` as a separate module and excludes it from
`./...` at the repo root. That is harmless here — `docs/` holds no Go code — but
do not be surprised when `go build ./...` skips it.

- [ ] **Step 1: Initialise the Hugo module and pull the theme**

```bash
cd docs
hugo mod init github.com/tunedev/go-resilient-commerce-lab/docs
hugo mod get github.com/adityatelange/hugo-PaperMod
cd ..
```

- [ ] **Step 2: Write `docs/hugo.toml`**

```toml
baseURL = "https://tunedev.github.io/go-resilient-commerce-lab/"
locale = "en-gb"
title = "go-resilient-commerce-lab"
enableRobotsTXT = true

[module]
  [[module.imports]]
    path = "github.com/adityatelange/hugo-PaperMod"

[params]
  description = "A Go microservices lab for distributed failure modes: idempotent request handling, a transactional outbox, and the traces that explain them."
  ShowReadingTime = true
  ShowCodeCopyButtons = true
  ShowPostNavLinks = true
  ShowBreadCrumbs = true

[[menu.main]]
  identifier = "posts"
  name = "Posts"
  url = "/posts/"
  weight = 10

[[menu.main]]
  identifier = "repo"
  name = "Source"
  url = "https://github.com/tunedev/go-resilient-commerce-lab"
  weight = 20

[markup.highlight]
  noClasses = false
  lineNos = false
```

- [ ] **Step 3: Write `docs/layouts/shortcodes/diagram.html`**

```html
{{ $name := .Get "name" }}
{{ $title := .Get "title" }}
{{ if not $name }}{{ errorf "diagram shortcode called without a name in %s" .Page.File.Path }}{{ end }}
{{ if not $title }}{{ errorf "diagram shortcode called without a title in %s" .Page.File.Path }}{{ end }}
<figure class="diagram">
  <iframe
    src="{{ .Site.BaseURL }}diagrams/{{ $name }}.html"
    title="{{ $title }}"
    loading="lazy"
    style="width:100%;height:{{ default "640" (.Get "height") }}px;border:0;border-radius:8px;">
  </iframe>
  <figcaption>{{ $title }}</figcaption>
</figure>
```

Used from a post as:

```
{{</* diagram name="system-topology" title="The twelve containers and their wires" */>}}
```

- [ ] **Step 4: Write `docs/content/_index.md` and `docs/content/posts/_index.md`**

`_index.md` introduces the lab in three sentences and links the repository.
`posts/_index.md` sets `title: Posts`. Neither carries content that will go
stale, so no counts and no roadmap.

- [ ] **Step 5: Write `.github/workflows/pages.yml`**

```yaml
name: pages

on:
  push:
    branches: [main]
  workflow_dispatch:

permissions:
  contents: read
  pages: write
  id-token: write

concurrency:
  group: pages
  cancel-in-progress: false

jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version: "1.27"
      - uses: peaceiris/actions-hugo@v3
        with:
          hugo-version: "0.165.0"
          extended: true
      - uses: actions/configure-pages@v6
      - run: hugo --minify --source docs
      - uses: actions/upload-pages-artifact@v5
        with:
          path: docs/public

  deploy:
    needs: build
    runs-on: ubuntu-latest
    environment:
      name: github-pages
      url: ${{ steps.deployment.outputs.page_url }}
    steps:
      - id: deployment
        uses: actions/deploy-pages@v5
```

`setup-go` is required before Hugo because the theme is a Hugo Module, which is
fetched with the Go module tooling.

- [ ] **Step 5b: Point the README at the published diagram**

Now that the site exists, add one line to the README's Topology section:

```markdown
The interactive version of this diagram is on the site; the README keeps a
static Mermaid one because GitHub renders it inline.
```

Task 15 deliberately left this out, because at that point neither the site nor
the diagram existed.

- [ ] **Step 6: Verify locally**

Run: `make site`
Expected: the site serves on `http://localhost:1313/go-resilient-commerce-lab/`
with the PaperMod theme and a Posts menu entry, and
`/architecture/system-topology/` renders the diagram iframe from Task 16.

- [ ] **Step 7: Commit and confirm the deploy**

```bash
git add docs .github/workflows/pages.yml
git commit -m "feat(docs): Hugo site with PaperMod and the Pages deploy workflow

The theme is a Hugo Module rather than a git submodule, so CI needs no
submodule checkout; hugo mod init writes a nested go.mod under docs, which Go
tooling excludes from the root module."
git push
```

Then watch the `pages` workflow and open the published URL. If it 404s, Pages is
not set to deploy from GitHub Actions.

---

### Task 15: PR template and README

**Closes:** PL-33.

**Files:**
- Create: `.github/PULL_REQUEST_TEMPLATE.md`
- Create: `README.md`

- [ ] **Step 1: Write `.github/PULL_REQUEST_TEMPLATE.md`**

```markdown
## Summary

## Why this change is needed

## What changed

## How it was tested

## Risk

## Rollback plan

## Observability impact
<!-- New spans, metrics or log fields. If a failure mode changed, say how it
     would be noticed in Jaeger, Prometheus or the logs. -->
```

- [ ] **Step 2: Write `README.md`**

Keep it short. Depth belongs in the spec and the posts.

Required sections:

1. **What this is** — two or three sentences.
2. **Quick start** — `make docker-up`, then
   `make scenario NAME=duplicate-order`, then the URLs: order
   `:8080/healthz` (there is no route at the bare root), Jaeger `:16686`,
   Prometheus `:9090`, Grafana `:3300`.
3. **Topology** — the inline Mermaid diagram below.
4. **The rules that keep it honest** — one line each: four separate logical
   databases so no cross-service transaction is possible; `internal/` holds no
   business rules; every state change commits with its outbox event; liveness
   checks nothing while readiness checks the pool.
5. **Make targets** — the table.
6. **Where things live** — link the design spec and the Epic A plan.

```mermaid
flowchart LR
  client[client / labctl] --> order[order-service]
  order --> orders[(orders db)]
  order -- OTLP --> collector[otel-collector]
  collector --> jaeger[Jaeger]
  prometheus[Prometheus] -- scrape /metrics --> order
  grafana[Grafana] --> prometheus
  grafana --> jaeger
```

The README keeps a static Mermaid diagram because GitHub renders it inline.

Do not mention the interactive diagram or the site here. Neither exists until
Tasks 14 and 16, and a README that describes a future deliverable as a present
fact is the overclaim this section is meant to avoid. Task 14 adds the pointer
once there is something to point at.

- [ ] **Step 3: Verify the quick start from a clean checkout**

```bash
git clean -xdn
make docker-up && make scenario NAME=duplicate-order && make docker-down
```

Expected: PASS. Fix the README if any step required knowledge that is not in it.

- [ ] **Step 4: Commit**

```bash
git add README.md .github/PULL_REQUEST_TEMPLATE.md
git commit -m "docs: README quick start and PR template

The PR template asks for rollback plan and observability impact, because a
change that alters a failure mode without changing what an operator would see
is the kind this lab exists to study."
```

---

### Task 16: the system-topology diagram

**Closes:** PL-34.

**Files:**
- Create: `docs/static/diagrams/system-topology.html`
- Create: `docs/content/architecture/system-topology.md` (the companion description)

- [ ] **Step 1: Build the diagram with the architecture-diagram skill**

Invoke the `architecture-diagram` skill. It produces a self-contained
interactive HTML file plus a companion Markdown description. Brief it with:

- **Nodes:** client/labctl, order-service, orders database (drawn distinctly from
  the three sibling databases inventory, payments, notifications, which are
  present but unused in Epic A), otel-collector, Jaeger, Prometheus, Grafana.
- **The step-through:** `POST /orders` with an Idempotency-Key, key claimed,
  order and items and outbox row committed in one transaction, `202` returned,
  outbox publisher drains the row, span exported to the collector, collector
  forwards to Jaeger, Prometheus scrapes `/metrics`.
- **Mode toggle:** synchronous command wires versus asynchronous event wires.
  In Epic A only the command path is live and the event path terminates at the
  log sink; the toggle should show that honestly rather than drawing a Kafka
  that does not exist yet.
- **The point the diagram must make:** four separate databases, and one
  transaction boundary around the order row, its items, its outbox event and its
  idempotency key.

- [ ] **Step 2: Verify it**

Open `docs/static/diagrams/system-topology.html` directly in a browser with no
web server. Expected: it renders with no network requests, the step-through
plays, and both light and dark themes are legible.

- [ ] **Step 3: Write the companion description**

`docs/content/architecture/system-topology.md` carries the companion Markdown
the skill emits, plus the shortcode that will embed the diagram:

```
{{</* diagram name="system-topology" title="The order path and the transaction boundary" */>}}
```

The shortcode cannot be rendered until Task 14 builds the site, so verifying the
iframe belongs there. Task 14 runs `make site` and confirms it loads.

- [ ] **Step 4: Commit**

```bash
git add docs/static/diagrams docs/content/architecture
git commit -m "docs: interactive system-topology diagram

The mode toggle contrasts the live synchronous command path with the event path,
which in Epic A terminates at the log sink rather than a broker."
```

---

### Task 17: posts one and two

**Closes:** PL-35, and closes Epic A.

**Files:**
- Create: `docs/content/posts/why-the-boring-parts-come-first.md`
- Create: `docs/content/posts/idempotency-keys-making-a-retried-post-safe.md`

Write these from what actually happened while building Tasks 1 through 16, not
from this plan. Where the implementation diverged from the plan, say so — the
divergences are the interesting part, and a build-along that only reports the
happy path is worth less than one that reports the corrections.

Both posts use the fixed structure as literal headings:

```
## Problem
## Decision
## Tradeoff
## Failure mode
## How I tested it
## How I observed it
## Run it yourself
```

- [ ] **Step 1: Write post one, "Why the boring parts come first"**

Cover, with real code from the repository:

- Single module, and why not one per service.
- Four logical databases: the constraint is enforced by the setup rather than by
  discipline, which is what makes the saga lesson in Epic D honest.
- Ports declared in `app/`, adapters under `adapter/`, `internal/` with no
  business rules.
- The argument for OpenTelemetry and CI at commit one instead of phases twelve
  and seventeen. The concrete payoff to name: the slog handler that had to wrap
  rather than embed `slog.Handler`, a bug that would have been found much later
  and in five services rather than one.
- `httpx.Route` and low-cardinality span names.
- Embed the `system-topology` diagram.
- **Run it yourself:** `make docker-up`, then the real trace fetched from
  Jaeger's API for a `POST /orders` request, quoted as output rather than a
  screenshot. A pasted terminal transcript a reader can reproduce beats an
  image they cannot.

- [ ] **Step 2: Write post two, "Idempotency keys: making a retried POST safe"**

Cover:

- The four branches: replay, `409`, `202` while in progress, `400` with no key.
- Why the request hash is canonicalised, and the `float64` limitation.
- The split design: the middleware claims with insert-or-nothing, the use case
  completes inside the order's transaction. Explain the two designs rejected —
  completing in a second transaction, and threading response bytes back out
  through the layers — and the window each leaves open.
- What `202` honestly means when a duplicate arrives mid-flight, and what
  happens to an `in_progress` row whose owner crashed.
- Releasing the claim on `5xx`, and why the release uses `context.WithoutCancel`.
- **Run it yourself:** `make scenario NAME=duplicate-order`, with its real
  output, and the three `POST /orders` spans fetched from Jaeger's API. Note
  what the trace does NOT show: there are no database spans in this epic, so
  the trace cannot reveal which request reached Postgres. Say that plainly
  rather than implying the trace proves more than it does. Per-query spans
  arrive in Epic G.

- [ ] **Step 3: Verify both posts render**

Run: `make site`
Expected: both appear under Posts, code blocks are highlighted, the diagram
iframe loads, images resolve.

- [ ] **Step 4: Run the full gate one last time**

```bash
make lint && make test-race && make integration
make docker-up && make scenario NAME=duplicate-order && make docker-down
```

Every command must pass before Epic A is closed.

- [ ] **Step 5: Commit, push, and close the epic**

```bash
git add docs/content/posts
git commit -m "docs: posts on foundations and on idempotency keys

Written from the build, including the two idempotency designs that were tried
and rejected and the window each of them left open."
git push
```

Confirm the `pages` workflow published both posts, then move PL-16 through
PL-35 and PL-7 to Done in Jira.

---

## Definition of done for Epic A

- `make lint`, `make test-race` and `make integration` all pass locally and in CI.
- `make docker-up` brings up twelve containers and `curl localhost:8080/healthz` answers.
- `make scenario NAME=duplicate-order` prints PASS and a Jaeger trace URL.
- A `POST /orders` trace is visible in Jaeger, named `POST /orders`, with the
  request's log lines carrying the same `trace_id`. The trace has one server
  span and no database child; that is expected in this epic.
- Prometheus reports the `order` target up and serves
  `http_server_request_duration` for it.
- Both posts are live on GitHub Pages with the interactive diagram embedded.
- Spec §7 has been updated to record the metrics-scrape deviation.
