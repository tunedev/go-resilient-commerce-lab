---
title: "Why the boring parts come first"
date: 2026-09-07T09:00:00+01:00
description: "Module layout, four separate databases, ports and adapters, and OpenTelemetry at commit one -- and the instrumentation bug that decision caught."
---

This is the first post in a build-along. The repository is a Go microservices
lab whose subject is distributed failure: client retries, duplicate events,
partial failure, payment uncertainty. None of that is in this post. This post is
about the scaffolding that comes before any of it, and about the bugs that
scaffolding turned up on its own.

{{< diagram name="system-topology" title="The order path and the transaction boundary" >}}

## Problem

The interesting part of a distributed system is what it does when something
breaks. You cannot study a break you cannot see, and you cannot see one you
cannot reproduce. So the usual build order -- features first, tracing around
phase twelve, CI around phase seventeen -- is backwards for a lab whose entire
output is legible failures.

It is also backwards for a reason that is easier to measure. Instrumentation is
cross-cutting: every service gets the same logger, the same tracer setup, the
same HTTP shell. A bug in that shared code is written once and copied N times.
Adding it late means finding it in N services. Adding it first means finding it
in one.

## Decision

**One Go module, not one per service.** `cmd/` holds binaries, `internal/` holds
the shared shell, and each service lives under `services/<name>/` split into
`domain/`, `app/` and `adapter/`. `go test ./...` covers everything, a refactor
that crosses a service boundary is one commit, and there is no replace-directive
bookkeeping. The isolation this project actually cares about is not enforced by
the module graph anyway.

**Four logical databases, created by the setup rather than by discipline.** The
Postgres container runs one init script:

```sql
CREATE DATABASE orders;
CREATE DATABASE inventory;
CREATE DATABASE payments;
CREATE DATABASE notifications;
```

Each service is handed a DSN naming exactly one of them. Three of the four have
no reader and no writer today. They exist so that when the saga arrives in a
later epic, "the order service could just update the inventory table in the same
transaction" is not a temptation someone resists -- it is a statement that does
not compile into a working query. A cross-database transaction here is not
discouraged. It is impossible. That is what makes the saga lesson honest instead
of performative.

**Ports declared where they are consumed, adapters underneath.** The interface
lives in `app/`, next to the use case that needs it, and the implementation
lives under `adapter/`:

```go
// Package app holds the order service use cases. Port interfaces are declared
// here, on the consumer side, and implemented under adapter/.
package app

// OrderStore persists orders.
type OrderStore interface {
	// CreateOrder writes the order, its items, the outbox events and, when
	// completion is not nil, the idempotency completion, in one transaction.
	// Either all of them exist afterwards or none of them do.
	CreateOrder(ctx context.Context, order domain.Order, events []outbox.Event, completion *idempotency.Completion) error

	// GetOrder returns the order, or domain.ErrOrderNotFound.
	GetOrder(ctx context.Context, id string) (domain.Order, error)
}
```

`internal/` holds no business rules. `internal/httpx` knows about status codes
and middleware; it does not know what an order is. `internal/idempotency` knows
how to claim a key; it does not know that the thing being created is an order.
The rule is easy to state and easy to check in review, and it is the reason the
idempotency middleware could be tested against a stand-in handler at all.

**OpenTelemetry and CI at commit one.** `internal/otelx` installs a tracer
provider exporting OTLP over gRPC and a meter provider backed by a Prometheus
registry, before any service exists to use them. The lint-and-test gate landed
in the scaffolding commit, before the first feature.

**Low-cardinality span names.** `otelhttp` wraps the whole mux, so it names a
span before routing has happened, and the routing is what knows the pattern. The
rename therefore has to happen inside the handler, once the mux has matched:

```go
// Route registers handler at pattern and renames the active server span to the
// route pattern. ServeMux does not tell an outer middleware which pattern
// matched, so the rename happens inside the registered handler, which closes
// over its own pattern, and the span name stays low cardinality.
func Route(mux *http.ServeMux, pattern string, handler http.Handler) {
	mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace.SpanFromContext(r.Context()).SetName(pattern)
		handler.ServeHTTP(w, r)
	}))
}
```

The result is `POST /orders` and `GET /orders/{id}` as operation names, not one
distinct operation per order id.

## Tradeoff

One module means one dependency set and one Go version for every service. You
cannot upgrade one service's `pgx` and leave the others behind, and a dependency
pulled in for one service is on every other service's build. In a real
deployment that matters. In a lab that is always built and tested as a unit, it
costs nothing and saves a lot of ceremony.

Four databases inside one container isolate transactions, not failure. Stopping
that container stops all four. The property being bought is "no shared
transaction", and only that property.

Instrumentation first costs commits that ship no user-visible behaviour at all.
Configuration loading, the logger and the tracer and meter providers all landed
before there was a service binary to run them. The HTTP shell and the first
endpoint that answers anything, `GET /healthz`, then arrived together in a
single commit -- by which point every request that endpoint would ever serve was
already traced and logged. That is a real price, paid up front, in exchange for
every later bug arriving already annotated.

## Failure mode

The payoff arrived immediately, and it was a bug I would not have found by
reading the code.

The obvious way to write a `slog` handler that adds trace ids is to embed the
inner handler and override the one method you care about:

```go
type traceHandler struct {
	slog.Handler // embedded: everything except Handle is promoted
}

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()))
	}
	return h.Handler.Handle(ctx, r)
}
```

This compiles, and it works, right up until someone writes `logger.With(...)`.
`With` calls `WithAttrs`, and the promoted `WithAttrs` belongs to the *inner*
handler, so it returns the inner handler -- a plain JSON handler with no trace
injection. Every log line from a derived logger silently loses its `trace_id`.
Nothing errors. The field is just absent.

The fix is to wrap rather than embed, and to re-wrap on every method that
returns a handler:

```go
type traceHandler struct {
	inner slog.Handler
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{inner: h.inner.WithGroup(name)}
}
```

Found in the only service that exists. Under the usual build order it would have
been found once the lab had grown its inventory, payment and notification
services -- after the same handler had been copied into all four.

Two more bugs in the same area, both found later on the branch and both worth
naming:

`log/slog.SetDefault` was never called. The logger was built with the service's
configuration and handed to the server, but three call sites -- including
`internal/httpx`'s JSON write error path and the fatal startup path in
`cmd/order/main.go` -- log through the package-level `slog` functions. Those
went to the stdlib default: plain text on stderr, no `service` field, no
`trace_id`. The startup path is exactly the one you need to be readable.

And `/metrics` is registered with `mux.Handle` rather than `httpx.Route`, so a
Prometheus scrape is not renamed to a route pattern. It still produces a span --
`otelhttp` wraps the whole mux, so everything does. Scrapes land under the bare
operation name `GET`. That is visible in Jaeger below, and it is worth knowing
before you go looking for a scrape by route name and conclude the exporter is
broken.

## How I tested it

The wrap-versus-embed bug has a specific shape: the handler works until a
derived logger is used. A test that only checks `Info` therefore passes against
the broken version. The test that discriminates has to call `With` first:

```go
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
```

That distinction -- between a test that passes and a test that would fail if the
code were wrong -- became the recurring theme of the epic, and the next thing it
caught was a claim I had written down as fact.

The outbox publisher claims due rows with `FOR UPDATE SKIP LOCKED`, and my plan
said, in as many words, that `SKIP LOCKED` is what stops two publishers
double-publishing the same event. There is a test asserting exactly that:

```go
func TestConcurrentPublishersDoNotDoublePublish(t *testing.T) {
	ctx, pool := setup(t)
	for range 20 {
		seedEvent(t, ctx, pool, "OrderCreated")
	}

	sink := &recordingSink{}
	// ... four publishers race for five seconds ...

	if got := sink.count(); got != 20 {
		t.Fatalf("sink saw %d publishes for 20 events; a claimed row was published twice", got)
	}
}
```

Green, every run. So I deleted `SKIP LOCKED` from the query and ran it again. It
was still green.

The claim was wrong. Plain `FOR UPDATE` *blocks* the second publisher on the
locked row; when the first transaction commits, the second re-evaluates its
`WHERE` clause under READ COMMITTED, no longer matches the now-`published` row,
and moves on. Correctness comes from the row lock and the status predicate.
`SKIP LOCKED` is a liveness property -- it lets the second publisher take
different work instead of waiting -- and this test never measured it.

I ran the same experiment on a test I was less sure of, the one asserting that a
failed idempotency completion rolls back the whole order. Moving `Complete` out
of the transaction made it fail. That one discriminates. The general rule I took
away: a test you have never seen fail is an assumption, not a test, and the
cheapest way to promote it is to break the code on purpose and watch.

Integration tests are behind a build tag and run real Postgres 18.6 in
testcontainers, so `go test ./...` stays fast and `make integration` is the slow
honest one. CI runs the same three checks -- `golangci-lint`, `go test -race`
and `go test -race -tags=integration` -- as separate jobs, plus a fourth that
brings the compose stack up and runs the scenarios against it.

## How I observed it

Bring the stack up and send some traffic:

```console
$ make docker-up
$ make scenario NAME=duplicate-order
running scenario duplicate-order: the same Idempotency-Key twice yields one order, not two
one order created: ord_1a0d7838-368a-4aef-b178-1e91d6415a87
PASS
trace: http://localhost:16686/search?service=order&start=1788872659582862&end=1788872899588166&limit=20
```

Then ask Jaeger's API what it received, rather than looking at a picture of it.
First, the operation names:

```console
$ curl -s "http://localhost:16686/api/traces?service=order&lookback=1h" \
    | jq -r '.data[].spans[].operationName' | sort | uniq -c | sort -rn
      5 GET
      3 POST /orders
      1 GET /readyz
      1 GET /orders/{id}
```

Four distinct names for a run that made ten requests. `GET /orders/{id}` is the
route pattern, not the path -- fetching a thousand different order ids would
still produce one operation name. That is `httpx.Route` working.

The five spans named `GET`, with no route at all, are the Prometheus scrapes:

```console
$ curl -s "http://localhost:16686/api/traces?service=order&operation=GET&lookback=1h" \
    | jq -r '.data[0].spans[0].tags[]
             | select(.key=="url.path" or .key=="user_agent.original")
             | [.key, .value] | @tsv'
url.path	/metrics
user_agent.original	Prometheus/3.14.0
```

`/metrics` is registered with `mux.Handle` instead of `httpx.Route`, so nothing
renames its span, and `otelhttp` falls back to the request method. Worth knowing
before you go hunting for a scrape by route name.

Now the three `POST /orders` spans, with their status, duration in microseconds,
and the number of parent references each one has:

```console
$ curl -s "http://localhost:16686/api/traces?service=order&operation=POST%20/orders&lookback=1h" \
    | jq -r '.data[] | .traceID as $t | .spans[]
             | [$t, .operationName,
                (.tags[]|select(.key=="http.response.status_code").value|tostring),
                (.duration|tostring), (.references|length|tostring)] | @tsv'
bda2e8668b417bd2af46392de1a97eb2	POST /orders	409	685	0
d76d4002973a466c43ab6253eb07a5c3	POST /orders	202	1013	0
17ebab48881385d192cade8e23912687	POST /orders	202	6619	0
```

Read that last column carefully, because the trace says less than a trace
usually says. Zero parent references everywhere, and one span per trace:

```console
$ curl -s "http://localhost:16686/api/traces?service=order&operation=POST%20/orders&lookback=1h" \
    | jq -r '.data[] | [.traceID, (.spans|length|tostring)] | @tsv'
bda2e8668b417bd2af46392de1a97eb2	1
d76d4002973a466c43ab6253eb07a5c3	1
17ebab48881385d192cade8e23912687	1
```

**There are no database spans in this epic.** `internal/postgres` emits none;
per-query tracing arrives in a later epic. So this trace cannot tell you which
request reached Postgres, how long the transaction took, or which statement was
slow. It tells you that a request arrived, what route it matched, what status it
returned, and how long the whole thing took. That is all. A screenshot of the
Jaeger timeline would look impressive and would be telling you exactly this
much.

What does connect the trace to the rest of the system is the trace id, which the
logger injects into every record whose context carries a span:

```console
$ docker compose -f deploy/docker-compose.yml logs order --no-log-prefix \
    | grep -F 17ebab48881385d192cade8e23912687 | jq .
{
  "time": "2026-09-07T23:22:34.025924612Z",
  "level": "INFO",
  "msg": "request",
  "service": "order",
  "method": "POST",
  "path": "/orders",
  "status": 202,
  "duration": 6469081,
  "request_id": "e310b0f6-4603-48d3-8ccc-420483211ab3",
  "trace_id": "17ebab48881385d192cade8e23912687",
  "span_id": "637212090b6e5f39"
}
```

The span reports 6619 microseconds and the log line reports 6469081 nanoseconds
for the same request: the span brackets the log middleware, so it is slightly
wider. Both are one request, joined by an id, and that join is the thing worth
having on day one.

Metrics take a different path entirely. The service exposes an OTel-produced
Prometheus registry on `/metrics` and Prometheus pulls it directly, so the
metric path never touches the collector:

```console
$ curl -s 'http://localhost:9090/api/v1/targets?state=active' \
    | jq -r '.data.activeTargets[] | [.labels.job, .health, .scrapeUrl] | @tsv'
order	up	http://order:8080/metrics
```

The request histogram is `http_server_request_duration_seconds`. Its labels come
from `otelhttp` and cover method, status code, scheme and server address -- but
not the route. Span names here are per-route; these metrics are not.

## Run it yourself

```bash
git clone https://github.com/tunedev/go-resilient-commerce-lab
cd go-resilient-commerce-lab
make docker-up
make scenario NAME=duplicate-order
```

`docker-up` starts six containers: Postgres, the order service, the OpenTelemetry
collector, Jaeger, Prometheus and Grafana. While it is up:

- order service: <http://localhost:8080/healthz>
- Jaeger: <http://localhost:16686>
- Prometheus: <http://localhost:9090>
- Grafana: <http://localhost:3300>

Every `curl` and `jq` invocation quoted above runs against that stack. Tear it
down with `make docker-down`, which also removes the volumes.

The next post is about the one feature this epic ships: making a retried `POST`
safe, the two designs for it that I built and threw away, and a bug in the seam
between two well-tested components that every per-task review missed.
