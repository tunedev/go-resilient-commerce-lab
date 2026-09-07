# go-resilient-commerce-lab — Design

Date: 2026-09-07
Repository: https://github.com/tunedev/go-resilient-commerce-lab
Module path: `github.com/tunedev/go-resilient-commerce-lab`

## 1. Purpose

A local, runnable Go microservices lab that reproduces the failure modes real
distributed backends face: client retries, duplicate events, partial failure,
payment uncertainty, inventory contention, crash-mid-flow, and latency that has
nothing to do with the query you are looking at.

The lab is a build-along. Each slice of work ships three things together: working
code, a one-command reproducible demo, and a blog post written from what actually
happened rather than from the plan.

### Success criteria

1. `docker compose up` brings the whole system up on a laptop.
2. Every failure scenario is reproducible by a single `labctl scenario <name>`
   command that asserts its own outcome and prints a Jaeger trace URL.
3. Every service is traced, logged with trace correlation, and scraped for
   metrics from its first commit.
4. CI is green on every merge to `main`, including race detection and integration
   tests against real Postgres and Redpanda.
5. Fifteen published posts, each reproducible by its reader.

## 2. Scope

v1 is sub-projects A through H (section 13). Each is independently shippable:
at every merge to `main` the lab runs and demonstrates something.

### Non-goals for v1

Kubernetes manifests, a real payment provider, authentication and authorisation,
a UI, multi-region concerns, and horizontal autoscaling. The Kubernetes incident
scenario (H) is a written incident-response exercise driven by simulated pod
behaviour, not a real cluster.

Alternatives to the chosen architecture are catalogued in section 14 and tracked
in the Jira Icebox epic. They are deliberately out of v1.

## 3. Architecture

### 3.1 Repository layout

Single Go module. Multi-module would mean `replace` directives and version churn
between services for no learning value.

```
go-resilient-commerce-lab/
  cmd/
    order/  inventory/  payment/  notification/
    mock-payment-provider/  mock-email-provider/
    labctl/
  internal/                 # shared infrastructure, zero business rules
    config/  httpx/  logger/  otelx/  postgres/
    outbox/  inbox/  idempotency/
    retry/  breaker/  broker/  worker/
  services/
    order/
      domain/               # states and transitions, pure, no I/O
      app/                  # use cases; port interfaces declared here
      adapter/              # pg repo, http handlers, kafka consumer, clients
      migrations/
    inventory/  payment/  notification/
  deploy/
    docker-compose.yml  Dockerfile
    otel-collector.yaml  prometheus.yml  grafana/
  docs/
    hugo.toml
    content/posts/  content/architecture/
    static/diagrams/
    superpowers/specs/
  .github/
    workflows/  PULL_REQUEST_TEMPLATE.md
  Makefile
```

Rules that keep the structure honest:

- **Port interfaces are declared in `app/`, not a separate `port/` package.** Go
  convention is to define an interface where it is consumed.
  `app.InventoryReserver` is satisfied by `adapter.HTTPInventoryClient` in
  production and by a hand-written fake in tests.
- **`internal/` contains no business rules.** If a package there knows what an
  order is, it belongs in `services/`.
- **One Dockerfile for all seven binaries**, multi-stage, parameterised by
  `ARG SERVICE`.

### 3.2 Runtime topology

Twelve containers.

| Group | Containers |
|---|---|
| Data | `postgres` — one container, four separate logical databases |
| Transport | `redpanda` |
| Services | `order` `inventory` `payment` `notification` |
| Mocks | `mock-payment-provider` `mock-email-provider` |
| Observability | `otel-collector` `jaeger` `prometheus` `grafana` |

Databases: `orders`, `inventory`, `payments`, `notifications`. Each service holds
only its own DSN. Cross-service joins and cross-service transactions are
physically impossible rather than merely discouraged — this is what makes the
saga lesson real.

Infrastructure chaos needs no container. It is applied to Postgres from outside
by `cmd/labctl`.

Loki sits behind an opt-in compose profile so that trace-to-logs correlation is
available without a mandatory thirteenth container.

### 3.3 Service anatomy

Identical in all four services, so the fourth costs nothing to reason about.

```
HTTP handler  ->  app use case  ->  domain transition (pure)
                       |
                       v
        single pg transaction: state row + outbox row
                       |
                       v
        outbox publisher worker  ->  Redpanda
                       |
                       v
        consumer  ->  inbox dedupe  ->  app use case
```

Every service exposes `/healthz`, `/readyz`, `/metrics`.

### 3.4 Technology choices

| Concern | Choice | Rationale |
|---|---|---|
| Routing | `net/http` stdlib, Go 1.22+ method patterns | `mux.HandleFunc("POST /orders", h)` removes the need for chi or gin |
| Postgres driver | `pgx/v5` + `pgxpool` | Current, fastest, native Postgres types |
| Query layer | Raw SQL | The atomic reservation `UPDATE` is a teaching artifact; it must be readable inline, not generated |
| Migrations | `goose/v3` with `embed.FS` | Runs at startup under a Postgres advisory lock so replicas do not race |
| Logging | `log/slog`, JSON handler | Stdlib; wrapped to inject `trace_id`/`span_id` from context |
| Tracing/metrics | OpenTelemetry Go SDK, OTLP/gRPC | Vendor neutral; collector fans out to Jaeger and Prometheus |
| Kafka client | `franz-go` | Best modern pure-Go client; speaks the Kafka API to Redpanda |
| Broker | Redpanda | Kafka API, single binary, no ZooKeeper, laptop friendly |
| Integration tests | `testcontainers-go` | Self-contained; no compose orchestration in CI |
| Site | Hugo + PaperMod via Hugo Modules | Single binary, no Node toolchain in a Go repo |

## 4. Data model

Only the tables that carry a lesson are specified here. Column lists are
indicative; migrations are authoritative.

### orders database

```
orders                 id, customer_id, status, total_amount, currency,
                       created_at, updated_at
order_items            order_id, sku, quantity, unit_price
order_sagas            id, order_id, current_step, status, last_error,
                       retry_count, next_attempt_at, created_at, updated_at
idempotency_keys       key, request_hash, resource_type, resource_id,
                       response_body, status_code, state, created_at
outbox_events          id, aggregate_type, aggregate_id, event_type, payload,
                       status, retry_count, next_attempt_at, created_at,
                       published_at, last_error
processed_events       event_id, event_type, processed_at
```

`idempotency_keys.key` is the primary key. `state` distinguishes `in_progress`
from `completed`, which is what allows a concurrent duplicate to receive `202`
rather than a partial result.

### inventory database

```
inventory_items          sku, available_quantity, reserved_quantity, version
inventory_reservations   id, order_id, sku, quantity, status, expires_at,
                         idempotency_key, created_at, updated_at
outbox_events            (as above)
processed_events         (as above)
```

The reservation is atomic in one statement, with no application-level lock:

```sql
UPDATE inventory_items
   SET available_quantity = available_quantity - $2,
       reserved_quantity  = reserved_quantity  + $2,
       version            = version + 1
 WHERE sku = $1
   AND available_quantity >= $2;
```

Zero rows affected means out of stock. A `CHECK (available_quantity >= 0)`
constraint is the second line of defence.

### payments database

```
payments           id, order_id, provider_reference, idempotency_key, amount,
                   currency, status, attempt_count, last_polled_at,
                   created_at, updated_at
outbox_events      (as above)
processed_events   (as above)
```

### notifications database

```
notifications      id, user_id, order_id, channel, status, provider_message_id,
                   idempotency_key, retry_count, created_at, updated_at
outbox_events      (as above)
processed_events   (as above)
```

## 5. Saga and event model

### 5.1 Commands are synchronous HTTP; events are asynchronous over Redpanda

Not everything goes through the broker. The order saga calls
`POST /inventory/reservations` and `POST /payments` directly and receives results
as events. This is the hybrid most production systems land on, and it lets the
lab teach both retry stories side by side: an HTTP command retried under a stable
idempotency key, versus an event redelivered to an idempotent consumer.

### 5.2 Order lifecycle

```
pending
  |- reserve_inventory      cmd -> inventory
  |- await_reservation      evt <- InventoryReserved | InventoryReservationFailed
  |- start_payment          cmd -> payment
  |- await_payment          evt <- PaymentSucceeded | PaymentFailed | PaymentUnknown
  |- confirm_order          commit reservation, evt -> OrderConfirmed

compensation:
  reservation failed  -> cancel order (nothing to compensate)
  payment failed      -> release reservation -> cancel order
  payment unknown     -> DO NOT compensate. Park in awaiting_reconciliation,
                         extend reservation TTL, let the reconciler decide.
  step timeout        -> retry with backoff; after cap -> manual_review
```

Order statuses: `pending`, `inventory_reserved`, `payment_pending`,
`payment_succeeded`, `payment_failed`, `confirmed`, `cancelled`, `expired`,
`awaiting_reconciliation`, `manual_review`.

`POST /orders` returns `202` with `{"order_id": ..., "status": "pending"}`. The
saga advances asynchronously.

### 5.3 Two saga drivers, both required

- **Event-driven advance** — the saga consumer reacts to result events. Handles
  the happy path.
- **Timer sweeper** — polls `order_sagas WHERE status='running' AND
  next_attempt_at <= now()`. Handles the case where the event never arrives,
  which is the case that matters.

### 5.4 Idempotency keys are derived, never random

```
key = saga_id + ":" + step
```

A retried command therefore carries an identical key by construction, with no
bookkeeping. At the API boundary the client supplies its own `Idempotency-Key`
header, stored with a hash of the request body:

| Condition | Response |
|---|---|
| Same key, same body, completed | Replay the stored response and status code |
| Same key, same body, in progress | `202` with current known state |
| Same key, different body | `409 Conflict` |
| No key | `400 Bad Request` |

### 5.5 Outbox

State change and event row commit in the same local transaction. The publisher
worker claims batches with:

```sql
SELECT * FROM outbox_events
 WHERE status IN ('pending','retrying') AND next_attempt_at <= now()
 ORDER BY created_at
 FOR UPDATE SKIP LOCKED
 LIMIT $1;
```

`SKIP LOCKED` is what allows multiple publisher replicas without
double-publishing. On failure the row moves to `retrying` with backoff; past the
retry cap it becomes `dead_lettered` with `last_error` recorded.

### 5.6 Inbox dedupe

In one transaction:

```sql
INSERT INTO processed_events (event_id, event_type, processed_at)
VALUES ($1, $2, now())
ON CONFLICT (event_id) DO NOTHING;
```

Zero rows affected means duplicate: commit and acknowledge, do nothing else.
Otherwise apply the state change in that same transaction. The event id is the
outbox row's UUID, carried as a Kafka record header, stable across every
redelivery.

### 5.7 The payment-unknown invariant

Releasing inventory while a charge may have landed is the bug this lab exists to
demonstrate. The invariant — an unknown payment must never reach the
compensation path — is enforced at four layers, roughly thirty lines in total:

| Layer | Mechanism |
|---|---|
| Compile time | `FailureCause` is a struct with an unexported field; only `CauseReservationFailed` and `CausePaymentDeclined` exist as package-level values, so no caller can construct an unknown cause |
| Runtime | The transition function returns an error on any illegal edge |
| Database | `CHECK` constraint on order status transitions, which catches manual SQL too |
| Test | Property-based test asserting no path exists from `awaiting_reconciliation` to a released reservation |

The compile-time layer is an approximation. Go has no sum types, so this is a
workaround for a missing language feature and the accompanying post says so
rather than presenting it as idiomatic Go.

### 5.8 Payment reconciliation

Payments in `unknown` are polled by a reconciliation worker against the provider
using the stored `provider_reference` or the original idempotency key.

```
processing -> unknown -> reconciling -> succeeded
processing -> unknown -> reconciling -> failed
processing -> unknown -> manual_review        (age cap exceeded)
```

The worker never re-charges. It only asks what happened.

### 5.9 A failure mode kept deliberately

A reservation whose TTL lapses while payment sits unknown, where payment later
confirms as succeeded. The system can neither fulfil nor silently refund, so the
order moves to `manual_review`. Most tutorials design this away. Keeping it is
honest and earns a post.

The saga extends `expires_at` while parked in `awaiting_reconciliation`, which
narrows the window without closing it. A reservation in `committed` state can
never be released by the expiry sweeper.

## 6. Chaos control plane

### Provider behaviour is selected by the request payload

Stripe-style magic values. No global mutable state, scenarios run concurrently
without interfering, and the same mechanism works unchanged inside the test
suite.

| `payment_method_id` | Mock payment provider behaviour |
|---|---|
| `pm_ok` | Charges, returns success |
| `pm_decline` | Returns declined — non-retryable |
| `pm_timeout_charged` | Charges, then hangs past the client timeout |
| `pm_timeout_uncharged` | Hangs past the client timeout, never charges |
| `pm_down` | `503` |
| `pm_slow_3s` | Succeeds after three seconds |

The mock email provider keys off the recipient address: `ok@`, `bounce@`,
`timeout-sent@`, `down@`.

### Infrastructure chaos is `labctl`

```
labctl chaos lock-row --sku ps5 --hold 30s
labctl chaos seed-unindexed --rows 2000000
labctl chaos saturate-pool --conns 50
labctl load --rps 50 --duration 60s
```

### Scenarios

`labctl scenario <name>` runs a complete demo end to end, asserts its outcome,
and prints the Jaeger trace URL. One command per post, reproducible on any
laptop.

```
duplicate-order        same key twice -> one order
last-playstation       concurrent buyers -> exactly one wins
timeout-but-charged    unknown -> reconciled -> confirmed, inventory never released
duplicate-event        redelivery -> no double state change
email-crash            at-least-once delivery, documented honestly
latency-lock           DB CPU low, time spent waiting on locks
```

## 7. Observability

Wired from the first commit, not added in a later phase. Retrofitting five
services is the failure mode this ordering avoids.

- **Traces.** OTel SDK in every service. `otelhttp` for inbound and outbound
  HTTP; manual spans around DB queries and Kafka produce/consume. OTLP/gRPC to
  the collector; the collector fans out to Jaeger.
- **Trace context across the broker.** W3C `traceparent` is injected into Kafka
  record headers on produce and extracted on consume. Omitting this kills every
  trace at the broker, which is the single most common instrumentation mistake
  in event-driven systems.
- **Parent-child spans, not span links, for consumed events.** Span links are
  more correct for long-running flows because a child span makes trace duration
  span the whole flow. Our sagas complete in seconds, and parent-child produces
  the single-trace view that makes the system legible. The accompanying post
  states this tradeoff and says when to switch.
- **Logs.** `log/slog` JSON with `trace_id`, `span_id`, `service`, `order_id`,
  `saga_id`, `event_id`. Loki behind an opt-in compose profile for
  trace-to-logs correlation.
- **Metrics.** RED per endpoint plus the ones that would actually page someone:
  `outbox_pending_age_seconds`, `dlq_depth`, `payment_unknown_total`,
  `reservation_conflict_total`, `breaker_state`, `provider_latency_seconds`,
  `db_query_duration_seconds`. Grafana dashboards provisioned as code in
  `deploy/grafana/`.

## 8. Testing strategy

Green must mean shippable. Four layers with distinct jobs.

- **Domain — pure, table-driven.** Order and payment state transitions, retry
  classification, backoff with jitter, circuit breaker transitions, request
  hashing. No I/O.
- **Use case — hand-written fakes** implementing the `app` port interfaces. No
  generated mocks: a test asserting "was called once" proves nothing about
  behaviour.
- **Handler — `httptest`** over the real router. Missing key, malformed JSON,
  same key with same body, same key with different body.
- **Integration — `testcontainers-go` against real Postgres and Redpanda.** The
  tests that would actually catch this being broken:
  - N goroutines buy the last unit; exactly one reservation succeeds and
    `available_quantity` never goes negative
  - State row and outbox row commit atomically; abort the transaction and
    neither exists
  - The same event delivered twice changes state once
  - `pm_timeout_charged` leaves payment `unknown`, the reconciler drives it to
    `succeeded`, and inventory was never released
  - Exceeding the retry cap produces `dead_lettered` with a recorded reason
- **Scenario — `labctl scenario`** against live compose, on `main` only.

Retry classification is itself under test. Retryable: timeouts, connection
reset, `5xx`, `429`. Not retryable: validation errors, insufficient funds,
invalid payment method, `401`, `403`.

## 9. CI

GitHub Actions. Module and build caches; a concurrency group cancels superseded
runs.

```
lint         golangci-lint (subsumes gofmt and go vet)
test         go test -race ./...
integration  testcontainers-go: real Postgres and Redpanda
build        docker build, matrix over the seven binaries
pages        hugo build -> deploy GitHub Pages    (main only)
```

Makefile targets: `test` `test-race` `lint` `fmt` `vet` `run` `docker-up`
`docker-down` `migrate` `scenario` `site`.

The PR template carries: Summary, Why, What changed, How it was tested, Risk,
Rollback plan, Observability impact.

## 10. Blog and diagrams

Hugo rooted at `docs/`, PaperMod via Hugo Modules. Deployed to GitHub Pages by
CI on merge to `main`.

Every post uses a fixed structure:

```
Problem / Decision / Tradeoff / Failure mode / How I tested it / How I observed it
Run it yourself:  labctl scenario <name>   plus the Jaeger trace screenshot
```

The "run it yourself" line is what separates these posts from the many other
saga write-ups: a reader reproduces the failure on their own laptop in one
command.

### The series

| Epic | Posts |
|---|---|
| A | Why the boring parts come first · Idempotency keys: making a retried POST safe |
| B | The last PlayStation problem: preventing oversell without a distributed lock |
| C | A timeout is not a failure: modelling payment uncertainty · Reconciliation: finding out what actually happened |
| D | Sagas without a framework · The compensation you must not run |
| E | The outbox pattern, and why SKIP LOCKED is the whole trick · Exactly-once delivery does not exist; exactly-once effect does |
| F | You cannot unsend an email: honest guarantees for external side effects · Retries that help versus retries that amplify |
| G | Your traces die at the broker, and how to fix it · What to actually put on the dashboard |
| H | DB CPU is low but you are waiting on Postgres: a latency triage guide · Eight percent error rate on some pods, and rollback removes the security fix |

### Diagrams

Built with the `architecture-diagram` skill, which emits a self-contained
interactive HTML file plus a companion Markdown description. Output goes to
`docs/static/diagrams/` and is embedded in posts by iframe shortcode. Small
static diagrams in the README and ADRs stay inline Mermaid.

| Diagram | Mode toggle carrying the lesson |
|---|---|
| `system-topology` | sync command wires versus async event wires |
| `order-saga-happy-path` | step-through only |
| `payment-timeout-charged` | `pm_timeout_charged` versus `pm_timeout_uncharged` — identical wires, opposite correct outcomes |
| `outbox-to-inbox` | single delivery versus duplicate delivery |
| `trace-propagation` | `traceparent` in Kafka headers on versus off |
| `latency-triage` | slow query versus lock contention versus pool starvation |

## 11. Jira structure

Project: **PL — GoResilienceLearnings** on `sanusiababatunde.atlassian.net`.

Nine epics: A through H, plus an Icebox. Docs and site work lives inside Epic A
rather than in an epic of its own.

**All nine epics are created up front; full stories are written for Epic A only.**
Eighty stories written now would go stale before they are reached, and would
contradict working incrementally. Each subsequent epic's stories are written at
its kickoff, informed by what the previous epic actually taught.

Blog posts and diagrams are stories inside their own epic, never a separate blog
track. An epic is not done until its post ships. That is what makes this a
build-along rather than a codebase someone intends to write about later.

Labels: `go` `docker` `saga` `outbox` `otel` `chaos` `ci` `blog` `diagram`

### Epics

| Key | Epic |
| --- | --- |
| PL-7 | A — Foundation and idempotency |
| PL-8 | B — Inventory and overselling |
| PL-9 | C — Payment and the unknown state |
| PL-10 | D — Saga orchestration |
| PL-11 | E — Outbox and duplicate events |
| PL-12 | F — Notifications and resilience patterns |
| PL-13 | G — Observability deepening |
| PL-14 | H — Chaos and debugging lab |
| PL-15 | Icebox — alternatives deliberately deferred |

### Epic A stories

```
PL-16  A1   Repo scaffolding: go.mod, Makefile, .golangci.yml, .gitignore, LICENSE
PL-17  A2   internal/config   — env-based configuration
PL-18  A3   internal/logger   — slog JSON with trace-correlation handler
PL-19  A4   internal/otelx    — tracer and meter providers, OTLP exporter, clean shutdown
PL-20  A5   internal/httpx    — server, graceful shutdown, error envelope, middleware chain
PL-21  A6   internal/postgres — pgxpool, goose migrations under advisory lock, tx helper
PL-22  A7   compose: postgres (four databases), otel-collector, jaeger, prometheus, grafana
PL-23  A8   Dockerfile (multi-stage, ARG SERVICE) and order service in compose
PL-24  A9   order domain: statuses and transitions, pure and table-tested
PL-25  A10  POST /orders happy path: handler -> app -> pg repo
PL-26  A11  internal/idempotency: store, request hashing, middleware (replay / 409 / 202)
PL-27  A12  GET /orders/{id}
PL-28  A13  /healthz, /readyz, /metrics
PL-29  A14  internal/outbox: table, WithTx write, publisher worker behind outbox.Sink
PL-30  A15  cmd/labctl skeleton and scenario duplicate-order
PL-31  A16  GitHub Actions: lint, test, integration, build
PL-32  A17  Hugo site with PaperMod, Pages deploy workflow
PL-33  A18  PR template and README architecture section
PL-34  A19  Diagram: system-topology
PL-35  A20  Posts 1 and 2
```

A14 carries the outbox groundwork that §13 requires in Epic A. Twenty stories,
not nineteen.

## 12. Delivery loop

The same seven steps for every epic.

```
1. Generate the epic's stories in Jira
2. Branch feat/<epic>
3. TDD the slice
4. labctl scenario <name> proves it end to end
5. Write the post from what actually happened, not from the plan
6. PR (template) -> CI green -> merge -> Pages publishes the post
7. Close the epic
```

## 13. Sub-projects

Ordered so that each depends only on what precedes it, and so that observability
and CI exist before there is anything to observe or break.

| Epic | Name | Ships | Demo |
|---|---|---|---|
| A | Foundation and idempotency | Repo, compose, Postgres, order-service, `POST /orders`, idempotency keys, OTel tracing, structured logs, CI, Hugo site | `duplicate-order` |
| B | Inventory and overselling | inventory-service, atomic reservation, commit, release, expiry sweeper | `last-playstation` |
| C | Payment and the unknown state | payment-service, mock payment provider with six modes, `unknown` status, reconciliation worker | `timeout-but-charged` |
| D | Saga orchestration | `order_sagas`, event-driven advance, timer sweeper, compensation, the four-layer unknown invariant, `manual_review` | full order flow, both outcomes |
| E | Outbox and duplicate events | Outbox tables, `SKIP LOCKED` publisher, Redpanda wiring, `processed_events` inbox dedupe | `duplicate-event` |
| F | Notifications and resilience | notification-service, notification outbox, mock email provider, backoff with jitter, retry classification, circuit breaker, DLQ with admin inspect and retry | `email-crash` |
| G | Observability deepening | Prometheus, Grafana dashboards as code, full metric set, trace propagation across Kafka, optional Loki profile | full-flow trace |
| H | Chaos and debugging lab | `labctl chaos` subcommands, slow query, lock contention, pool starvation, simulated bad deploy, written incident notes | `latency-lock` |

Redpanda enters the compose file in Epic E. Before that, the outbox publisher
worker ships in Epic A behind an `outbox.Sink` interface whose only
implementation logs the event and marks the row published. Epic E adds a
`KafkaSink` and changes one wiring line. The transactional-write pattern is
therefore in place from the first commit, and only the transport changes later.

Each epic gets its own implementation plan when it starts. This document is the
program-level design; it is not itself an implementation plan for all eight.

## 14. Icebox — alternatives deliberately deferred

Tracked as an unscheduled Jira epic and documented in
`docs/content/architecture/alternatives.md`.

| Alternative | What changes | Why it is interesting |
|---|---|---|
| Durable execution (Temporal, Restate, DBOS) | The saga becomes ordinary sequential code; the engine persists and replays each step | Deletes the saga table, sweeper, retry bookkeeping and state machine. The sequel post — "the 400 lines Temporal deletes, and what you lose by letting it" — is stronger than either version alone |
| Pure choreography | No orchestrator; services react to events and emit events | Trivially extensible, but the business process exists in no single place and "why is this order stuck" becomes archaeology across topics |
| Async commands over the broker | Commands go on a command topic; participants reply with events | Real backpressure, no synchronous coupling; costs immediate validation feedback and needs request/reply correlation. The NServiceBus / MassTransit / Axon shape |
| Debezium CDC instead of outbox polling | The WAL becomes the event source; no poller | No polling lag to tune, but needs Kafka Connect, and raw WAL rows are not domain events — in practice teams still write an outbox table and CDC that |
| Span links instead of parent-child | Consumed events start a new trace linked to the producing span | Correct for long-running flows; loses the single-trace demo |
| 2PC / XA | Actual distributed transactions | The thing sagas exist to avoid. One explanatory section, not an implementation |

Vocabulary worth recording: a *saga* in the original Garcia-Molina sense is a
sequence of local transactions with compensations. A stateful component that
routes and tracks the flow is properly a *process manager*. What is commonly
called an orchestrated saga is a process manager.

## 15. Open items

- Jira epics and Epic A stories exist in PL. Stories for epics B through H are
  written at each epic's kickoff, not now.
- GitHub Pages must be enabled for the repository with source set to GitHub
  Actions before the `pages` workflow can deploy.
