---
title: "Idempotency keys: making a retried POST safe"
date: 2026-09-07T18:00:00+01:00
description: "Four branches, one insert-or-nothing, and the completion that commits with the order -- plus two designs I built and threw away, and the bug that lived in the seam between them."
---

The [previous post]({{< ref "why-the-boring-parts-come-first" >}}) was about the
scaffolding that came before any feature. This is the feature: a client can
retry `POST /orders` and get one order.

It is a small thing to describe and a fiddly thing to get right. Four separate
bugs on this branch lived in it, and the worst of them survived every per-task
review that saw the code.

## Problem

A client sends `POST /orders`, the order commits, and the connection drops
before the response gets back. The client now knows nothing. Retrying might
create a second order; not retrying might lose the first. There is no answer the
client can work out on its own, because the information it needs never left the
server.

The standard fix is to move the decision to the server and let the client name
the attempt. The client generates a key, sends it on every retry of the same
logical request, and the server promises: one key, one result.

## Decision

`POST /orders` requires an `Idempotency-Key` header. `GET /orders/{id}` does
not, because a read is already safe to repeat.

```go
// Register wires the order routes. Only POST /orders requires an
// Idempotency-Key; a read is already safe to repeat.
func Register(mux *http.ServeMux, h *Handler, store *idempotency.Store) {
	httpx.Route(mux, "POST /orders", idempotency.Require(store)(http.HandlerFunc(h.Create)))
	httpx.Route(mux, "GET /orders/{id}", http.HandlerFunc(h.Get))
}
```

The middleware decides between five outcomes. It has other early returns -- an
unreadable body, a body that is not JSON, a failed claim -- but these are the
five the design turns on.

| Situation | Response |
| --- | --- |
| No `Idempotency-Key` header | `400 idempotency_key_required` |
| Key unseen | claim it, run the handler |
| Key seen, same request, completed | replay the stored status and bytes, plus `Idempotent-Replay: true` |
| Key seen, same request, still running | `202 {"status":"processing"}` |
| Key seen, different request | `409 idempotency_key_reused` |

**The claim is an insert-or-nothing.** Election happens in the database, in one
statement, with no read-then-write race to reason about:

```sql
INSERT INTO idempotency_keys (key, request_hash, state)
VALUES ($1, $2, 'in_progress')
ON CONFLICT (key) DO NOTHING
RETURNING key
```

If a row comes back, this request owns the key. If `RETURNING` yields nothing,
someone else got there first, and only then does the code read the existing row
to decide between replay, conflict and in-progress. The primary key on `key` is
the whole concurrency control.

**The request is hashed after canonicalisation.** A retrying client often
reserialises its request rather than replaying the exact bytes -- a Go map
iterates in a different order, a proxy reindents the JSON -- and none of that is
a different request:

```go
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

`encoding/json` marshals a `map[string]any` with its keys sorted, which is what
makes this work. It also has a limitation worth naming out loud: unmarshalling
into `any` turns every JSON number into a `float64`, and re-marshalling prints
that float back. Numbers above 2^53 do not survive:

```
{"quantity":1}             -> {"quantity":1}
{"quantity":1.0}           -> {"quantity":1}
{"a":1,"b":2}              -> {"a":1,"b":2}
{"b":2,"a":1}              -> {"a":1,"b":2}
{"id":9007199254740992}    -> {"id":9007199254740992}
{"id":9007199254740993}    -> {"id":9007199254740992}
```

The first four rows are the feature. The last two are the bug: two genuinely
different request bodies canonicalise to the same bytes, so the second one is
served as a replay of the first instead of being refused with a `409`. For an
order body carrying customer ids, skus and small quantities that is harmless.
For a body carrying a 64-bit integer identifier it is not, and the fix -- a
canonicaliser using `json.Decoder` with `UseNumber`, or hashing over a real
canonical-JSON encoder -- is not in this epic.

**The claim and the completion are split across two layers, deliberately.** The
middleware claims the key on the pool, before the handler runs. The completion
is written by the use case, inside the same transaction as the order:

```go
// Complete flips a claimed key to completed inside the caller's transaction,
// so the key and the resource it names commit together.
func Complete(ctx context.Context, tx pgx.Tx, c Completion) error {
```

`Complete` takes a `pgx.Tx`, not a pool. It cannot be called outside a
transaction, and the transaction it joins is the order's:

```go
func (s *Store) CreateOrder(ctx context.Context, order domain.Order, events []outbox.Event, completion *idempotency.Completion) error {
	return infrapg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		// insert the order
		// insert one row per item
		// append the OrderCreated outbox event
		if completion != nil {
			if err := idempotency.Complete(ctx, tx, *completion); err != nil {
				return err
			}
		}
		return nil
	})
}
```

Four writes, one commit: the order, its items, its outbox event, and the
completion of the key that authorised it. Either all four exist or none do.

### Two designs I built and threw away

**Complete the key in a second transaction, after the handler returns.** This is
the obvious shape -- the middleware owns the key, so the middleware should
finish it -- and it is where I started. The window it leaves is one line wide:
the order transaction commits, and the process dies before the second
transaction runs. Now the order exists and the key is still `in_progress`. Every
retry under that key gets `202 processing`, forever, and the client never learns
the order id of the order it successfully created. The failure is silent, and it
is worst exactly when it matters: under a crash, which is the thing idempotency
exists to survive.

**Thread the response bytes back out to the middleware.** The variant of the
same idea: buffer whatever the handler writes, and have the middleware store
those captured bytes against the key. It removes the need for the app layer to
know the response shape, and it leaves precisely the same window -- the capture
still happens after the order's transaction has committed, so a crash in between
strands the key the same way. It adds a second problem on top: the middleware
has to decide which responses count as "the result", which is a business
question the middleware has no business answering.

Both designs fail for the same reason. Two commits cannot be made atomic by
being adjacent. The only version with no window is the one where the key's
completion is part of the order's commit, which means the completion has to
travel *down* into the transaction rather than the response travelling *up* out
of it.

## Tradeoff

The price is that the app layer now knows what an HTTP response looks like.
`services/order/app/create_order.go` imports `net/http` and builds this:

```go
completion = &idempotency.Completion{
	Key:          idempotencyKey,
	ResourceType: "order",
	ResourceID:   order.ID,
	StatusCode:   http.StatusAccepted,
	ResponseBody: body,
}
```

A status code and a serialised response body, in the use case. In a strict
reading of ports and adapters that is a layering violation, and I am not going
to pretend otherwise. I took it because the alternative is a window in which an
order exists that its own client can never be told about, and because the leak
is one struct with an obvious name rather than an HTTP concept smeared through
the domain.

The second tradeoff is that `202 processing` is a genuinely unhelpful response,
and it is the honest one. More on that next.

## Failure mode

Four things went wrong here. Two threatened byte-exact replay, and two stranded
claims. Only three of them ever ran; the first was caught while writing the
migration.

**The plan specified `jsonb` for the stored response, and it was wrong.** That
is the reflexive choice, and it is what I had written down before writing any
code. But `jsonb` is a parsed representation: Postgres canonicalises on storage
and does not preserve key order. Measured on the Postgres 18.6 this lab runs:

```console
$ psql -c "SELECT '{\"order_id\":\"ord_1\",\"status\":\"pending\"}'::json AS as_json;"
                 as_json
-----------------------------------------
 {"order_id":"ord_1","status":"pending"}

$ psql -c "SELECT '{\"order_id\":\"ord_1\",\"status\":\"pending\"}'::jsonb AS as_jsonb;"
                  as_jsonb
--------------------------------------------
 {"status": "pending", "order_id": "ord_1"}
```

A replay is supposed to hand back the bytes the client first received. With
`jsonb` it would hand back different bytes that happen to mean the same thing --
fine for a client that parses JSON, wrong for a client that compares strings,
computes a signature, or diffs a log.

This one never shipped. The column was declared `json` in the same commit that
first created it, because writing the migration was the moment the question
"what exactly does a replay return?" became concrete enough to answer. The plan
was corrected to match the code rather than the other way round, which is the
happier direction. `outbox_events.payload` is `jsonb` and stays that way,
deliberately: consumers parse it, nobody compares its bytes, and if anything
ever needs to query inside a payload, `jsonb` is the type that can.

**`WriteJSON` broke the same property a second way.** It streamed through
`json.NewEncoder(w).Encode(v)`. `Encode` always appends a trailing newline. The
replay path writes the stored bytes raw, and those bytes came from
`json.Marshal`, which does not. So the first response and its replay were
identical except for one byte at the end -- the careful choice of column type
undone one layer up, by the response writer. `WriteJSON` now marshals and
writes:

```go
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
```

One value, one byte sequence, on every path. There is a bonus in the reordering:
`WriteHeader` now happens *after* the marshal, so a marshal failure can still
send a 500 instead of discovering the problem halfway through a committed 200.

**A panic stranded the claim.** `Recovery` is installed once at server level,
around the whole mux. `Require` is applied per route, inside it. So a handler
panic unwinds past `Require` on its way out to `Recovery`, and any release
written as a straight-line statement after `next.ServeHTTP` never runs. The key
stays `in_progress` forever. The fix is a `defer`, which runs during unwinding
and does not swallow the panic:

```go
capture := &statusCapture{ResponseWriter: w}
handlerReturned := false

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
```

`handlerReturned` is load-bearing, not defensive padding. Without it, a handler
that writes a `200` and *then* panics would look like a success to the status
check, and the claim would be kept for a request that never completed. The flag
is only ever set on the line after `ServeHTTP` returns normally.

The release runs on `context.WithoutCancel(ctx)`, which is the important detail
in that block. The reason a claim needs releasing is often that the request went
wrong, and one of the ways a request goes wrong is that the client hung up --
which cancels `r.Context()` immediately. Passing the cancelled context to
`Release` would fail the `DELETE` with `context canceled` and strand exactly the
claim it was written to free. `WithoutCancel` keeps the context's values and
drops its cancellation, so cleanup outlives the request that needed it.

**And a `400` stranded it too.** This is the one worth dwelling on.

The release originally fired only at `>= 500`, on the reasoning that a 5xx is a
transient failure worth retrying and a 4xx is the client's fault. But the order
handler returns `400` for validation failures and malformed JSON, and `422` for
an unknown sku. A request with an empty `customer_id` therefore claimed the key,
failed validation, returned `400` -- and left the key `in_progress` with no
order behind it. Every subsequent retry under that key, including the corrected
one, got `202 processing`. Forever: there is no TTL column and no sweeper in
this epic.

The middleware now releases on any non-2xx. The principle is that an idempotent
result is a *result*: a request that was rejected before it did anything has
none to record, so the key should go back to being unused and the client should
be free to correct the body and retry under it.

What makes this bug interesting is not the fix but where it lived. Both halves
were individually correct. The middleware's contract -- "release on failure" --
was implemented exactly as written. The handler's contract -- "400 for a bad
request" -- was implemented exactly as written. The bug existed only in the
composition, where one component's definition of failure did not cover the
other's. It survived every per-task review that saw the code, because every one
of those reviews looked at one component at a time, and neither component was
wrong.

### The parts that are still broken

Two, and neither has a workaround in this epic.

**`202 processing` is less informative than it looks.** It does not mean "your
order is being created". It means: a request carrying this key is in flight and
has not recorded a result. The duplicate learns nothing about whether the first
request will succeed, and the body carries no order id, because the middleware
does not have one -- the id is created inside the handler it did not run. The
client's only move is to retry until it gets a replay or a conflict.

**A claim whose owner crashed is stuck.** The deferred release runs inside the
process that made the claim. If that process is killed -- OOM, SIGKILL, node
loss -- nothing runs it. The row stays `in_progress`, and because `Claim` only
distinguishes completed from in-progress, every retry under that key gets `202`
indefinitely. The `idempotency_keys` table has no expiry column and there is no
reaper. Recovery today is deleting the row by hand. The real fix is a TTL plus a
sweeper that releases stale claims, and it is not written yet. I would rather
name that than describe the system as if it were.

## How I tested it

The `400` bug is the reason there is now an integration test at the seam.

Before it, the coverage looked complete and was not. The handler had unit tests
with no middleware. The middleware had integration tests against a stand-in
handler. `Register` -- the only code in the repository that composes the real
handler with the real middleware -- had no test at all, and it is exactly where
the bug lived.

```go
func TestRegisterInvalidBodyReleasesTheClaim(t *testing.T) {
	mux := newRegisteredMux(t)

	invalidBody := `{"customer_id":"","payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}`
	rec := doRequest(mux, "key-invalid", invalidBody)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}

	retry := doRequest(mux, "key-invalid", registeredValidBody)
	if retry.Code != http.StatusAccepted {
		t.Fatalf("retry under the same key status = %d, want 202: %s; the claim was stranded",
			retry.Code, retry.Body.String())
	}
}
```

`newRegisteredMux` builds the route table through `Register` itself, against a
throwaway Postgres in testcontainers, so the middleware and the handler are
composed the same way the running service composes them. The lesson generalises:
if a component is only reachable in production through a composition, test the
composition, because "both parts are tested" is not the same claim as "the thing
is tested".

The byte-exactness has its own guard, in the scenario rather than a unit test:
`labctl` compares the replayed body to the original with `string(second.Body) !=
string(first.Body)`. That comparison is why the trailing-newline bug had to be
fixed before the scenario would pass -- the fix landed three commits ahead of
the scenario that would have caught it.

## How I observed it

Every branch, against the running stack. No key:

```console
$ curl -sS -i -X POST localhost:8080/orders \
    -H 'Content-Type: application/json' -d "$BODY"
HTTP/1.1 400 Bad Request
{"error":{"code":"idempotency_key_required","message":"the Idempotency-Key header is required"}}
```

With a key, the first time:

```console
$ curl -sS -i -X POST localhost:8080/orders \
    -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-key-1' -d "$BODY"
HTTP/1.1 202 Accepted
{"order_id":"ord_b976246b-07ed-4428-975a-f9410d689cfe","status":"pending"}
```

The same key and the same body again -- note the header, and that the id is the
first order's:

```console
$ curl -sS -i -X POST localhost:8080/orders \
    -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-key-1' -d "$BODY"
HTTP/1.1 202 Accepted
Idempotent-Replay: true
{"order_id":"ord_b976246b-07ed-4428-975a-f9410d689cfe","status":"pending"}
```

The same key with the object's keys shuffled -- the canonicalising hash treats it
as the same request:

```console
$ curl -sS -i -X POST localhost:8080/orders \
    -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-key-1' \
    -d '{"items":[{"quantity":1,"sku":"playstation-5"}],"payment_method_id":"pm_ok","customer_id":"cust_lab"}'
HTTP/1.1 202 Accepted
Idempotent-Replay: true
{"order_id":"ord_b976246b-07ed-4428-975a-f9410d689cfe","status":"pending"}
```

The same key with a genuinely different body:

```console
$ curl -sS -i -X POST localhost:8080/orders \
    -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-key-1' \
    -d '{"customer_id":"someone_else","payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}'
HTTP/1.1 409 Conflict
{"error":{"code":"idempotency_key_reused","message":"this Idempotency-Key was used with a different request body"}}
```

And the regression every per-task review missed -- a validation failure followed
by a corrected retry under the same key:

```console
$ curl -sS -i -X POST localhost:8080/orders \
    -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-key-2' \
    -d '{"customer_id":"","payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}'
HTTP/1.1 400 Bad Request
{"error":{"code":"invalid_request","message":"customer_id is required"}}

$ curl -sS -i -X POST localhost:8080/orders \
    -H 'Content-Type: application/json' -H 'Idempotency-Key: demo-key-2' -d "$BODY"
HTTP/1.1 202 Accepted
{"order_id":"ord_9018c618-d2eb-41bb-a009-3566c86bc4c5","status":"pending"}
```

Before the fix, that second request answered `202 {"status":"processing"}` and
kept answering it.

The stored row is the replay, verbatim:

```console
$ psql -c "SELECT key, state, status_code, response_body FROM idempotency_keys WHERE key = 'demo-key-1';"
    key     |   state   | status_code |                               response_body
------------+-----------+-------------+----------------------------------------------------------------------------
 demo-key-1 | completed |         202 | {"order_id":"ord_b976246b-07ed-4428-975a-f9410d689cfe","status":"pending"}
```

The scenario asserts the load-bearing part of this -- the replay's status, its
`Idempotent-Replay` header, its body compared byte for byte against the
original, and the `409` for a reused key -- and exits non-zero if any of it
stops being true:

```console
$ make scenario NAME=duplicate-order
running scenario duplicate-order: the same Idempotency-Key twice yields one order, not two
one order created: ord_1a0d7838-368a-4aef-b178-1e91d6415a87
PASS
trace: http://localhost:16686/search?service=order
```

Three `POST /orders` requests go out during that scenario. On a stack started
fresh and used for nothing else, Jaeger has exactly three spans for them:

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

The durations are suggestive: 6619 microseconds for one `202` and 1013 for the
other. It is tempting to read that as "the first one hit the database and the
second one replayed from a cheaper path". **The trace does not say that.** The
final column is the parent-reference count, and it is zero on all three, because
each of these traces contains exactly one span. There are no database spans in
this epic -- `internal/postgres` is not instrumented, and per-query tracing
arrives in a later epic. Both requests touched Postgres; the trace cannot tell
you what either of them did there, or that one wrote and one only read. The
duration gap is consistent with the story and is not evidence for it.

What the trace does establish is that three separate requests arrived, all
matched the same route, and returned `202`, `202` and `409`. That only one order
came out of them is established elsewhere: the scenario compares the two `202`
bodies byte for byte, so both name the same order id, and then fetches that id
and requires a `200`.

## Run it yourself

```bash
make docker-up
make scenario NAME=duplicate-order
```

To poke at it by hand, with the stack up:

```bash
BODY='{"customer_id":"cust_lab","payment_method_id":"pm_ok","items":[{"sku":"playstation-5","quantity":1}]}'

curl -sS -i -X POST localhost:8080/orders \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: my-key' -d "$BODY"

curl -sS -i -X POST localhost:8080/orders \
  -H 'Content-Type: application/json' -H 'Idempotency-Key: my-key' -d "$BODY"
```

The `psql` output above came from inside the Postgres container:

```bash
docker compose -f deploy/docker-compose.yml exec postgres \
  psql -U lab -d orders -c "SELECT key, state, status_code, response_body FROM idempotency_keys;"
```

Tear it down with `make docker-down`.

That closes the first epic. Everything here is one service and one database, so
every duplicate could be settled by a primary key. The next epic on the plan is
inventory and overselling, which adds a second service -- and a class of
duplicate that no single unique index can decide.
