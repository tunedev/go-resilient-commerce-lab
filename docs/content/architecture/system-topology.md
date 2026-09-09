---
title: "System topology"
description: "The order path, the four separate databases, and the transaction boundary that holds one order together."
---

An interactive walk through what runs today: one client, two services, four
separate databases (two of them written to), two background publishers, and
the telemetry stack. Pick a path from the tab bar and step through it; drag
nodes to rearrange, and press `T` to switch themes.

{{< diagram name="system-topology" title="The order path and the transaction boundary" >}}

## The point of the picture

`POST /orders` writes four rows: the order, its item rows, its `OrderCreated`
outbox row, and the completion of its idempotency key. All four are written
inside one transaction against one database, and the diagram draws that boundary
as the loudest thing on the canvas. It is a boundary in both directions: nothing
inside it can commit alone, and nothing outside it can join.

`inventory` is now written to, but by the inventory service's own transactions,
never by the order-service transaction above: nothing in this diagram's order
path reads or writes it. `payments` and `notifications` remain created by the
same Postgres container with nothing reading or writing them at all. All three
are drawn because their separateness is what makes the boundary meaningful: a
transaction spanning two of them is not discouraged, it is impossible.

## Components

| Node | What it is |
| --- | --- |
| `labctl` | The Go CLI that drives the lab and asserts its own outcomes. |
| `order-service` | Serves `POST /orders`, `GET /orders/{id}`, `/healthz`, `/readyz` and `/metrics` on `:8080`. This diagram walks its write path. |
| `inventory-service` | Serves `PUT /inventory/items/{sku}`, `POST /inventory/reservations`, the commit and release routes, `/healthz`, `/readyz` and `/metrics` on `:8080` (published on host port `8081`). The order service never calls it; nothing in this diagram's paths does either. |
| `orders` database | Holds `orders`, `order_items`, `outbox_events` and `idempotency_keys`. |
| `inventory` database | Holds `inventory_items`, `inventory_reservations` and its own `outbox_events`. Read and written only by inventory-service. |
| `payments`, `notifications` | Separate databases in the same Postgres container. No reader, no writer. |
| outbox publishers | One goroutine per service, each in its own process, each claiming its own database's due outbox rows with `FOR UPDATE SKIP LOCKED` and handing them to a sink. |
| expiry sweeper | A goroutine in the inventory-service process. Claims reservations past `expires_at` with `FOR UPDATE SKIP LOCKED`, expires them and returns their quantity to available stock. Has no order-service counterpart. |
| log sink | The only `Sink` implementation. Writes a structured log line and reports success. Used by both publishers. |
| `otel-collector` | Receives OTLP over gRPC on `:4317`, batches, and exports to Jaeger. A traces pipeline only. |
| Jaeger | Trace storage and UI on `:16686`. |
| Prometheus | Scrapes `order:8080/metrics` and `inventory:8080/metrics` directly on a five second interval. |
| Grafana | Published on host port `3300`. Provisioned with Prometheus and Jaeger as datasources. |

There is no message broker in this system, and no payment or notification
service. The event path ends at the log sink.

## The paths

### Create an order

1. `labctl` sends `POST /orders` with an `Idempotency-Key`. The header is
   required on this route.
2. The middleware claims the key with an insert-or-nothing. Exactly one
   concurrent request gets a row back and owns the work.
3. The transaction opens and the order row is inserted in the `pending` state.
4. One row per line item is inserted.
5. The `OrderCreated` event is appended to `outbox_events`.
6. The claimed key flips to `completed`, storing the exact response bytes, and
   the transaction commits.
7. `202 Accepted` returns with the order id and status.

### Repeat the key

The same key with the same body loses the claim, reads the stored row, and gets
the stored status code and body back verbatim under `Idempotent-Replay: true`.
No handler runs, so no second order exists.

The same key with a different body is refused with `409 idempotency_key_reused`.
The body is canonicalised before it is hashed, so a reordered but equivalent
JSON body still matches.

### Drain the outbox

The publisher ticks, opens a transaction, and claims up to a batch of due rows
with `FOR UPDATE SKIP LOCKED`, which lets a second publisher take different rows
rather than block on the same ones. Each claimed event goes to the sink; a
success marks the row `published`, a failure records the error, increments the
attempt count and pushes `next_attempt_at` out by a doubling backoff. Past the
retry limit the row is marked `dead_lettered` and logged.

### Traces and metrics

Spans are batched and exported over OTLP to the collector, which forwards them
to Jaeger. Each request produces one server span named after its route pattern;
there are no per-query database spans. Metrics take a different path entirely:
Prometheus pulls `/metrics` from each service, so the metric path never touches
the collector. Grafana reads both.

## The mode toggle

**Command wires** shows the synchronous request and response path. **Event
wires** adds the outbox publisher and the log sink, and relabels
`outbox_events` as what it is on that path: a queue that lives inside the
database. Switching to event wires does not reveal a broker, because there is
none.
