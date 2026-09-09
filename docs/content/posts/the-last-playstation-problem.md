---
title: "The last PlayStation problem"
date: 2026-09-09T09:00:00+01:00
description: "Sixteen buyers, one console, and a reservation that lives in a single SQL statement -- plus the concurrency tests that looked correct and proved nothing until someone broke the code and watched."
---

The [previous post]({{< ref "idempotency-keys-making-a-retried-post-safe" >}}) was
about one client retrying one request. Everything there could be settled by a
unique index, because every duplicate carried the same key and the database
could see it.

This one is the opposite shape. Sixteen buyers send sixteen genuinely different
requests, each with its own idempotency key, each perfectly valid on its own.
There is nothing duplicated for an index to catch. They just all want the same
row.

## Problem

One console in stock. Sixteen people press buy at the same moment. The obvious
implementation is the one everybody writes first:

```
SELECT available_quantity FROM inventory_items WHERE sku = 'playstation-5';
-- 1, plenty, go ahead
UPDATE inventory_items SET available_quantity = available_quantity - 1 ...;
INSERT INTO inventory_reservations ...;
```

Read, decide, write. The decision is made against a value that was true when it
was read and need not still be true when it is written. Sixteen transactions can
all read `1`, all decide they are the winner, and all write.

That window is small, and small is the entire difficulty: it is small enough
that the naive version works on a laptop, works in a demo, works under a load
test that never quite overlaps, and then does not work on the day the console
launches. The whole epic is about making that window impossible rather than
unlikely, and -- harder -- about proving the tests that say so would actually
notice if it came back.

A note on scope, because the picture that description usually brings to mind is
bigger than what is here. This epic builds one new service, `inventory`, with
its own database, its own migrations and its own port. The order service does
not call it. There is no saga, no message broker, and no cross-service
transaction anywhere in this post. Every race described below happens inside one
service against one table.

## Decision

The check and the decrement are the same statement, and the check lives in the
`WHERE` clause:

```go
// Reserve guards the stock decrement in the UPDATE's WHERE clause, so no
// read-then-write window exists between checking availability and taking it.
func (s *Store) Reserve(ctx context.Context, r domain.Reservation, events []outbox.Event) error {
	return infrapg.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		const updateStock = `
UPDATE inventory_items
   SET available_quantity = available_quantity - $2,
       reserved_quantity  = reserved_quantity  + $2,
       version            = version + 1,
       updated_at         = now()
 WHERE sku = $1
   AND available_quantity >= $2`

		tag, err := tx.Exec(ctx, updateStock, r.SKU, r.Quantity)
		if err != nil {
			return fmt.Errorf("inventory: reserve stock %s: %w", r.SKU, err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrInsufficientStock
		}
```

There is no moment between the check and the take, because there is no check.
There is one statement that either matches a row and updates it or matches
nothing, and `RowsAffected() == 0` is the whole of "someone got there first".

The reason a single statement is enough is a property of the database rather
than of the Go around it, and it is worth watching once rather than trusting.
Two sessions, one unit of stock; the first holds its transaction open for two
seconds after taking the unit, and the second runs the identical guarded
`UPDATE` a third of a second later:

```console
# session A: take the unit, then hold the transaction open for two seconds
$ psql -c "BEGIN;" \
       -c "UPDATE inventory_items SET available_quantity = available_quantity - 1
             WHERE sku = 'playstation-5' AND available_quantity >= 1;" \
       -c "SELECT pg_sleep(2);" -c "COMMIT;" &

# session B, a third of a second later: the identical statement
$ psql -c "\timing on" \
       -c "UPDATE inventory_items SET available_quantity = available_quantity - 1
             WHERE sku = 'playstation-5' AND available_quantity >= 1;"
Timing is on.
UPDATE 0
Time: 1695.170 ms (00:01.695)
```

The second session did not read a stale value and act on it. It blocked on the
first session's row lock for the remaining 1.7 seconds, and when the first
committed it re-evaluated its own `WHERE` clause against the newly committed row
-- which now says `available_quantity = 0`, so the predicate no longer matches
and the statement reports `UPDATE 0`. That is the `RowsAffected() == 0` branch,
observed from the outside. The serialisation is Postgres's; the code's only job
is to not open a window it can drive a truck through.

The same trick guards every other state change. A reservation moves
`reserved -> committed`, `reserved -> released` or `reserved -> expired`, and
each transition carries its precondition in SQL:

```go
const update = `
UPDATE inventory_reservations
   SET status = $2, updated_at = now()
 WHERE id = $1 AND status = 'reserved'
RETURNING id, order_id, sku, quantity, status, expires_at, idempotency_key, created_at, updated_at`
```

So releasing an already-committed reservation is not prevented by the domain
layer remembering to check. It fails the same `WHERE` clause that everything
else fails, one layer below anything a caller can skip.

Repeat requests are handled by a plain unique column, not by middleware. There
is no idempotency middleware on this service: `inventory_reservations.idempotency_key`
is `NOT NULL UNIQUE`, and the insert that would collide with it happens inside
the same transaction as the stock decrement. A repeat of a key that already
produced a reservation reads that reservation back and answers `200`; a first
use answers `201`.

## Tradeoff

The thing this design buys is the absence of machinery. There is no distributed
lock, no lock service to run, no lease to renew, no coordination round trip on
the hot path, and nothing to reason about when the lock service is the component
that fails. Sixteen concurrent reservations cost sixteen transactions against
one row, and nothing else.

The price is that the entire guarantee is one line of SQL inside a string
literal, and nothing in Go's type system holds it there. Deleting
`AND available_quantity >= $2` compiles. It passes review if the reviewer is
reading the diff for style. It is exactly the kind of line a refactor tidies
away while "simplifying the query", and the system it breaks does not break
loudly -- it breaks on the busiest day, for the small number of requests that
happen to overlap.

That is why the tests for this line are mutation-proven rather than merely
green, and why most of the rest of this post is about tests rather than about
the feature.

There is a second, smaller price. The guarded `UPDATE` cannot tell "no such row"
apart from "not enough stock" -- both match nothing -- so a request for a sku
that does not exist gets `409 insufficient_stock` rather than `404`.
Distinguishing them would cost a second query on the failure path, and the
failure path is the one under load. I took the cheaper answer and am naming it
here so nobody reads that `409` as a diagnosis of stock levels.

## Failure mode

**Reserved stock leaks if nobody ever commits.** A reservation takes the unit
out of `available_quantity` and puts it in `reserved_quantity`. If the buyer
closes the tab, the console is held by a row nobody will ever act on. So
reservations carry `expires_at` (15 minutes by default) and a sweeper runs every
30 seconds, expiring what is due and returning the quantity to available stock.

**The sweeper's safety rested on a lock nothing tested.** Its expiry was the
only state change in the file with no `WHERE` guard and no `RowsAffected`
check; it updated the row by id and trusted `FOR UPDATE SKIP LOCKED` on the
claim query to stop a second sweeper from holding the same row. Deleting
`SKIP LOCKED` left every test in the package green -- nothing in the suite
noticed. A direct probe of two concurrent sweeps over one due reservation, run
40 rounds with a second live reservation present so that `reserved_quantity`
stayed positive and the `CHECK` constraint could not mask the damage, then found
39 of 40 rounds crediting the expired quantity to `available_quantity` twice.
Phantom stock: an oversell manufactured by the component whose job is to prevent
one.

The committed code scored 0 of 40. What was missing was the proof and not the
correctness -- but a correctness that lives entirely in a lock nothing tests is
one tidy-up away from gone, and it makes `SKIP LOCKED` look like the thing
holding the system together when it is not. The fix is the same fix as
everywhere else in this file -- put the precondition in the statement:

```go
const updateReservation = `
UPDATE inventory_reservations
   SET status = 'expired', updated_at = now()
 WHERE id = $1 AND status = 'reserved'`
```

With the guard in place and `SKIP LOCKED` still deleted, the same probe scored
0 of 40. The guard alone carries the safety. `SKIP LOCKED` stayed, because it is
still worth having: it lets a second sweeper take different work instead of
queueing behind the first. It is a liveness device, not a correctness one --
which is precisely the correction the [first post]({{< ref "why-the-boring-parts-come-first" >}})
had to make about the outbox publisher, arrived at again in a second place by
the same method.

**A reservation whose hold lapses while the payment outcome is unknown is a real
hole, and this epic does not close it.** Fifteen minutes pass, the sweeper
returns the unit to stock, and somewhere a payment is still in flight. If it
succeeds, the order is confirmed against stock that has already been sold to
somebody else. Nothing here prevents that. It needs a decision the inventory
service cannot make alone -- the whole payment-uncertainty problem -- and that
is a later epic. I would rather name it than describe this as finished.

## How I tested it

Every concurrency test in this epic had to be broken on purpose before it could
be trusted, and more than one of them did not survive the attempt. The method
never varied: break the code, run the test, watch. What varied was the reason
each test failed to notice.

**The shipped test.** Sixteen goroutines, distinct idempotency keys, one unit of
stock, released together at a barrier:

```go
var ready, release sync.WaitGroup
ready.Add(buyers)
release.Add(1)

var wg sync.WaitGroup
results := make([]error, buyers)
for i := range buyers {
	wg.Add(1)
	go func(i int) {
		defer wg.Done()
		ready.Done()
		release.Wait()
		results[i] = store.Reserve(ctx, reservations[i], events[i])
	}(i)
}

ready.Wait()
release.Done()
wg.Wait()
```

The distinct keys are load-bearing. With one shared key the fifteen losers would
be rejected by the unique index -- correct output, for the wrong reason, proving
nothing about stock at all.

It asserts one success, fifteen `ErrInsufficientStock`, and the database state
afterwards: `available_quantity = 0`, `reserved_quantity = 1`, one reservation
row, one outbox event. Deleting `AND available_quantity >= $2` from the query and
running it, plus the single-threaded insufficient-stock test alongside it:

```console
$ go test -tags=integration \
    -run 'TestReserveForMoreThanAvailableReturnsInsufficientStockAndChangesNothing|TestConcurrentBuyersOfTheLastUnit' \
    ./services/inventory/adapter/postgres/...
--- FAIL: TestConcurrentBuyersOfTheLastUnit (1.92s)
    concurrency_test.go:102: insufficientStock = 0, want 15
    concurrency_test.go:105: other errors = 15, want 0
--- FAIL: TestReserveForMoreThanAvailableReturnsInsufficientStockAndChangesNothing (1.31s)
    store_test.go:119: err = inventory: reserve stock sku-b: ERROR: new row for relation
        "inventory_items" violates check constraint "inventory_items_available_quantity_check"
        (SQLSTATE 23514), want ErrInsufficientStock
FAIL
```

Read the second failure closely, because it is the more interesting one. Without
the guard the `UPDATE` matches unconditionally, so every request succeeds until
one drives `available_quantity` below zero and trips the column's own `CHECK`.
The request does not fail with a clean domain sentinel; it fails with a raw
SQLSTATE from the last line of defence. The test discriminates precisely because
it insists on `ErrInsufficientStock` and nothing else.

**The barrier was not enough.** That failure took two attempts to get. The
test's first draft -- same sixteen goroutines, same barrier, same assertions,
but no pool warming -- passed eleven times out of eleven *with the guard
removed*. Instrumenting the mutated
`Reserve` with a timestamp around its `SELECT` showed why: the Go barrier released
all sixteen goroutines at the same instant, but only one of them already held a
pooled connection. The other fifteen were still opening TCP connections and
authenticating while the winner's entire transaction ran to commit. They arrived
five to seven milliseconds late, read `available = 0`, and correctly reported
insufficient stock. The race window was never entered. The test was measuring
connection setup.

The fix is `warmPool`, which runs `n` concurrent trivial queries before the race
so the pool already holds idle connections:

```go
// warmPool runs n concurrent queries so the pool ends up holding
// min(n, MaxConns) idle physical connections.
```

That comment is the second version. The first said it forced `n` idle
connections, which is not true: `pgxpool` defaults `MaxConns` to
`max(4, NumCPU)` and nothing in this repository sets it, so pinned to one CPU
`warmPool(16)` yields four. Four concurrent readers still detect the break -- the
mutation was re-run at `MaxConns=4` and failed every time -- so the code was
fine and only the claim was wrong. In a test whose entire purpose is to stop
overstated correctness claims, an overstated claim about the test's own
mechanism is the one thing that cannot stand.

**A test that was deleted.** The fix round for the sweeper added a
two-goroutine test over concurrent `ExpireDue` calls. It was run
against a mutation matrix: guard removed, pass; `SKIP LOCKED` downgraded to plain
`FOR UPDATE`, pass; both, pass; the claim's locking deleted entirely, pass three
times; locking deleted *and* guard removed, pass fifteen times. It discriminates
on nothing. Two goroutines with no rendezvous do not overlap -- the first
finishes before the second's query lands -- so the dangerous window is never
entered, exactly as in the case above but one layer up. Everything it asserted
was already covered single-threaded, more thoroughly, by an existing test. It was
deleted. A test that survives the removal of both mechanisms it appears to be
about is worse than no test, because it advertises a guarantee it does not check.

What replaced it is a test on the unexported `expireOne` in a package-internal
file, calling it twice with the same stale reservation value -- what a second
sweeper would have held. It reproduces the double credit deterministically, with
no goroutines at all. With the guard removed:

```
second expireOne reported success against an already-expired row
AvailableQuantity = 19, want 15
ReservedQuantity = 1, want 5
```

Reaching for an unexported function is not free, and it was taken deliberately:
while `SKIP LOCKED` is intact, no interleaving of two real `ExpireDue` calls can
hand the same row to two `expireOne` calls, so the property is only reachable
below the locking layer. The alternatives were a test seam in production code or
no test at all.

**One more thing worth saying plainly.** There is an older thirty-goroutine test
over ten units that also discriminates, and I originally described the new
sixteen-buyer test as complementary coverage. No mutation was found that the new
test catches and the old one misses. It is not additional coverage; it is the
sharp, deterministic, single-unit demonstration of the same property. Calling it
more than that would have been the same species of error as the `SKIP LOCKED`
sentence in the first post.

## How I observed it

The demonstration is a scenario `labctl` runs against the live stack: set stock
to one, fire sixteen concurrent reservations, and assert exactly one `201` and
fifteen `409 insufficient_stock`.

```console
$ make scenario NAME=last-playstation
running scenario last-playstation: sixteen buyers racing for one console produce exactly one winner
16 buyers: 1x201, 15x409
winner: reservation res_ec31dd57-b2f0-4614-a2d9-6bd677f2e824
losers: 15 buyers got 409 insufficient_stock
PASS
trace: http://localhost:16686/search?service=inventory&start=1788941808077734&end=1788942048137102&limit=20
```

The row and the events behind that run:

```console
$ psql -c "SELECT sku, available_quantity, reserved_quantity, version FROM inventory_items
             WHERE sku='playstation-5';"
      sku      | available_quantity | reserved_quantity | version
---------------+--------------------+-------------------+---------
 playstation-5 |                  0 |                 1 |       1

$ psql -c "SELECT event_type, count(*) FROM outbox_events GROUP BY event_type ORDER BY event_type;"
         event_type         | count
----------------------------+-------
 InventoryReservationFailed |    15
 InventoryReserved          |     1
```

One console, one reservation, one `InventoryReserved`. The fifteen rejections
are recorded too, each in its own transaction -- there is no state for a failed
reservation to be atomic with, since it changed nothing.

### The scenario that was vacuously green

The interesting part is that this demonstration protected nothing for a while,
and not intermittently. Run against an inventory service whose reservation had
been turned back into a read-then-write, in the exact sequence CI uses --
`down -v`, `up -d`, run the scenarios -- it passed three times out of three.

It is the same failure as the barrier test, one layer further out. `MinConns` is
unset, so a freshly started container holds exactly the one connection its
readiness ping created. Fifteen of the sixteen handlers therefore paid TCP,
Postgres startup and SCRAM authentication *inside the request*, before ever
reaching the vulnerable read. Measured with `httptrace`, the client delivered all
sixteen requests within about 1.5 milliseconds, so the client was not the
problem: the winner committed at around 11ms while the losers' read did not run
until 15 to 20ms. They never overlapped. Every loser legitimately read zero and
legitimately got a `409`, and the assertion was satisfied by code that was
broken.

The fix is in the demo client, not in production config: `warmService` fires one
concurrent `/readyz` per buyer between seeding the stock and starting the race,
which forces the server's pool to build a physical connection for each. It is
the out-of-process twin of `warmPool`. With it, the same restart-then-one-run
trial detects the break every time; without it, it detects nothing.

That makes four instances in this project of one class of bug -- a test that
cannot tell working code from broken -- each one found at a layer below the
last. Statement semantics: the first epic's outbox test, which passed with
`SKIP LOCKED` deleted because plain `FOR UPDATE` is also correct. Goroutine
scheduling: the deleted sweeper test, whose two goroutines never overlapped.
Connection acquisition: the barrier test, whose losers were still authenticating
when the winner committed. And connection acquisition again, one process further
out, in the demonstration itself.

### The two ways to break it

With the guard restored, I broke the reservation in two different ways to see
what the demonstration actually shows.

**Read-then-write.** Replace the guarded `UPDATE` with a `SELECT` and an
unconditional decrement -- the naive version from the top of this post:

```console
$ make scenario NAME=last-playstation
running scenario last-playstation: sixteen buyers racing for one console produce exactly one winner
16 buyers: 1x201, 4x409, 11x500
FAIL: buyer got unexpected status 500: {"error":{"code":"internal_error","message":"internal error"}}
exit status 1
```

Eleven `500`s, and the Postgres log says what they are:

```console
$ docker compose -f deploy/docker-compose.yml logs postgres | grep "check constraint"
ERROR:  new row for relation "inventory_items" violates check constraint "inventory_items_available_quantity_check"
```

The race is real, but no console is oversold. Each loser's `SELECT` reads `1`
and decides to proceed; its `UPDATE` then blocks on the winner's row lock, and
when it finally runs it decrements the newly committed zero to minus one and is
stopped by the column's `CHECK`. What the demonstration shows here is the shadow
of the bug rather than the bug: the database's last line of defence firing
eleven times in a single run.

**A stale read written back as an absolute value.** The break the `CHECK` cannot
catch: read `available_quantity`, subtract in Go, and write the result. Every
buyer whose read lands before a winner commits computes `1 - 1 = 0` and writes
zero, so the column never goes negative and no constraint is violated:

```console
$ make scenario NAME=last-playstation
running scenario last-playstation: sixteen buyers racing for one console produce exactly one winner
16 buyers: 12x201, 4x409
FAIL: got 12 winners, want exactly 1
exit status 1

$ psql -c "SELECT sku, available_quantity, reserved_quantity FROM inventory_items
             WHERE sku='playstation-5';"
      sku      | available_quantity | reserved_quantity
---------------+--------------------+-------------------
 playstation-5 |                  0 |                12
```

Twelve reservations against one console, and a row that says so: nothing
available, twelve held. That is the actual failure this epic exists to prevent,
and it is the one no constraint in the schema can catch, because every individual
write is perfectly legal. Both breaks were invisible while the connection pool
was cold.

### What the traces do and do not say

Every one of the sixteen requests is a span. Fetched from Jaeger's API rather
than squinted at in the UI, ordered by start time, showing the status, the offset
in microseconds from the first span, the duration, and the number of parent
references:

```console
$ curl -s "http://localhost:16686/api/traces?service=inventory\
&operation=POST%20/inventory/reservations&lookback=1h&limit=30" \
  | jq -r '[.data[].spans[] | {code: (.tags[]|select(.key=="http.response.status_code").value),
                               start: .startTime, dur: .duration, refs: (.references|length)}]
           | (map(.start)|min) as $t0
           | sort_by(.start)[]
           | [(.code|tostring), ((.start-$t0)|tostring), (.dur|tostring), (.refs|tostring)] | @tsv'
201	0	11192	0
409	149	19163	0
409	1388	20517	0
409	1447	17772	0
409	2428	16794	0
409	2612	19285	0
409	2836	19075	0
409	2840	16429	0
409	2892	19119	0
409	3010	11632	0
409	3102	18631	0
409	3117	18774	0
409	3381	18550	0
409	3473	15580	0
409	3509	20495	0
409	3544	20305	0
```

Sixteen requests, all starting inside 3.5 milliseconds of each other, every one
of them still running when the winner finishes at 11.2ms. That is the useful
reading: the requests genuinely overlapped, so the `409`s were decided by
contention and not by arriving late. It is also exactly the property the cold
pool destroyed, now visible from outside the process.

**The trace does not show what happened in the database.** The final column is
the parent-reference count and it is zero on every row, because each of these
traces contains exactly one span. `internal/postgres` emits no spans in this
epic; per-query tracing is a later one. It is tempting to read the losers'
durations -- most of them 15 to 20 milliseconds against the winner's 11 -- as
time spent blocked on the winner's row lock, and that is consistent with the
`psql` experiment at the top of this post. The trace is not the evidence for it.
What the trace establishes is that sixteen requests overlapped and how they
ended; everything below the HTTP handler is inferred from somewhere else.

## Run it yourself

```bash
make docker-up
make scenario NAME=last-playstation
```

The scenario asserts its own outcome and exits non-zero if the count is ever
anything but one winner and fifteen losers, so it is a regression test rather
than a demo you have to read.

By hand, with the stack up -- the inventory service is on port 8081:

```bash
curl -sS -i -X PUT localhost:8081/inventory/items/playstation-5 \
  -H 'Content-Type: application/json' -d '{"available_quantity":1}'

curl -sS -i -X POST localhost:8081/inventory/reservations \
  -H 'Content-Type: application/json' \
  -d '{"order_id":"ord_1","sku":"playstation-5","quantity":1,"idempotency_key":"k1"}'

curl -sS -i -X POST localhost:8081/inventory/reservations \
  -H 'Content-Type: application/json' \
  -d '{"order_id":"ord_2","sku":"playstation-5","quantity":1,"idempotency_key":"k2"}'
```

The first reservation answers `201`, the second `409 insufficient_stock`.
Repeating the first request verbatim answers `200` with the same reservation id,
because the key was already used.

The `psql` output above came from inside the Postgres container, against the
inventory database:

```bash
docker compose -f deploy/docker-compose.yml exec postgres \
  psql -U lab -d inventory \
  -c "SELECT sku, available_quantity, reserved_quantity FROM inventory_items;"
```

Tear it down with `make docker-down`.

To watch a test discriminate, delete `AND available_quantity >= $2` from
`services/inventory/adapter/postgres/store.go` and run
`go test -tags=integration ./services/inventory/adapter/postgres/...`. That is
the whole method, and it is still the only one that has reliably caught anything
on this project. The first post's rule survives the second epic intact: a test
you have never seen fail is an assumption, not a test.
