# Epic B — Inventory and Overselling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A second service that reserves stock without a distributed lock, where
concurrent buyers of the last unit produce exactly one winner and stock never
goes negative.

**Architecture:** `services/inventory/{domain,app,adapter}` mirroring
`services/order`, on its own logical database. The reservation guard lives in a
single SQL `UPDATE`'s `WHERE` clause, not in application code. Every state change
commits with its outbox event in one transaction.

**Tech Stack:** Go 1.27, `net/http` stdlib routing, pgx v5, goose v3, OpenTelemetry,
testcontainers-go. Every `internal/` package from Epic A is reused unchanged.

**Spec:** `docs/superpowers/specs/2026-09-07-go-resilient-commerce-lab-design.md`

**Jira:** Epic PL-8. Stories PL-36 … PL-47. Each task names the story it closes.

## How this plan differs from Epic A's, and why

Epic A's plan carried complete, never-compiled implementation bodies. Every one
of the fourteen defects found during execution was in that code, and several
were pure body-level errors — a `defer` inside a retry loop, a response writer
that appended a trailing newline, a provider leaked on an error path — that a
compiler and a test catch instantly.

So this plan specifies **exact interfaces, exact SQL, exact test cases and exact
behaviour**, and leaves routine bodies to the implementer, who has a compiler and
I do not. Code appears here only where the precise form is itself the deliverable:
the atomic reservation, the claim query, the transition table.

This is a deliberate departure from the writing-plans skill's "code blocks
required for code steps". Implementers are not being asked to guess: every
signature, every SQL statement, every test name and assertion is given. What is
withheld is only the mechanical translation between them.

## Global Constraints

- Module path `github.com/tunedev/go-resilient-commerce-lab`. Go 1.27.0.
- **No new dependencies.** Everything Epic B needs is already in `go.mod`. If you
  reach for a `go get`, stop and report — it means a task is off-plan.
- `services/inventory/domain` imports **only** the standard library. Prove it with
  `go list -deps`.
- `internal/` holds no business rules and its comments name no business entity.
  Epic B adds two config fields there and nothing else.
- Errors wrapped `fmt.Errorf("...: %w", err)`.
- **No emojis anywhere**: code, comments, log or print statements, commit messages.
- Comments state current behaviour only. No rationale narrative, no counterfactuals,
  no numbers that go stale. One short clause where a rationale is load-bearing.
- Tests use the standard library `testing` package only. No assertion library.
- **Every commit stands alone.** Verify the commit, not the working tree:

  ```bash
  T=$(mktemp -d) && git archive HEAD | tar -x -C "$T" && (cd "$T" && go build ./... && go test ./...) ; rm -rf "$T"
  ```

- **Never `git reset`.** Commits on this branch that are not yours must survive.
- Grafana is on host port **3300**. The order service holds **8080**; inventory
  takes **8081**.

## Contracts from Epic A, read from source

These exist and are reviewed. Use them; do not reimplement.

```go
// internal/postgres
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error)
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error
func WithTx(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error
func StartPostgres(t *testing.T) *pgxpool.Pool          // //go:build integration

// internal/outbox
func NewEvent(aggregateType, aggregateID, eventType string, payload any) (Event, error)
func Append(ctx context.Context, tx pgx.Tx, events ...Event) error
func NewPublisher(pool *pgxpool.Pool, sink Sink, logger *slog.Logger, opts PublisherOptions) *Publisher
func (p *Publisher) Run(ctx context.Context) error
type LogSink struct{ Logger *slog.Logger }
const Schema string                                      // outbox_events DDL

// internal/httpx
func Route(mux *http.ServeMux, pattern string, handler http.Handler)
func WriteJSON(ctx context.Context, w http.ResponseWriter, status int, v any)
func WriteError(ctx context.Context, w http.ResponseWriter, status int, code, message string, details any)
func Health() http.Handler
func Ready(check func(context.Context) error) http.Handler
func Metrics(registry *prometheus.Registry) http.Handler
func NewServer(o Options) *Server
func (s *Server) Run(ctx context.Context) error

// internal/config
func Load(serviceName string) (Config, error)

// internal/otelx
func Setup(ctx context.Context, serviceName, otlpEndpoint string) (*Providers, error)
```

`WithTx` returns `fn`'s error **unwrapped**, so callers match their own sentinels
with `errors.Is`.

## File structure

```
internal/config/config.go              MODIFIED: two new fields
services/inventory/
  domain/reservation.go                states, transitions, Validate
  domain/errors.go                     sentinels
  app/ports.go                         InventoryStore, declared consumer-side
  app/reserve.go                       Reserve use case
  app/manage.go                        Commit, Release, SetStock, GetReservation
  adapter/postgres/store.go            the atomic UPDATE and friends
  adapter/http/handler.go              decode, validate, delegate, encode
  adapter/http/routes.go               Register
  migrations/00001_inventory.sql       items and reservations
  migrations/00002_infrastructure.sql  outbox_events
  migrations/migrations.go             //go:embed *.sql
cmd/inventory/main.go                  composition root
cmd/labctl/last_playstation.go         the scenario
deploy/docker-compose.yml              MODIFIED: inventory service
deploy/prometheus.yml                  MODIFIED: inventory scrape target
docs/content/posts/...                 the post
```

---

### Task 1: inventory domain — states and transitions

**Closes:** PL-36.

**Files:**
- Create: `services/inventory/domain/reservation.go`, `domain/errors.go`, `domain/reservation_test.go`

**Interfaces produced:**

```go
type Status string
const (
    StatusReserved  Status = "reserved"
    StatusReleased  Status = "released"
    StatusCommitted Status = "committed"
    StatusExpired   Status = "expired"
)
var AllStatuses []Status

type Reservation struct {
    ID             string
    OrderID        string
    SKU            string
    Quantity       int
    Status         Status
    ExpiresAt      time.Time
    IdempotencyKey string
    CreatedAt      time.Time
    UpdatedAt      time.Time
}

type Item struct {
    SKU               string
    AvailableQuantity int
    ReservedQuantity  int
    Version           int64
}

func (s Status) CanTransitionTo(next Status) bool
func (r *Reservation) TransitionTo(next Status) error
func Validate(orderID, sku string, quantity int, idempotencyKey string) error

var (
    ErrIllegalTransition   error
    ErrReservationNotFound error
    ErrItemNotFound        error
    ErrInsufficientStock   error
    ErrMissingOrderID      error
    ErrMissingSKU          error
    ErrMissingKey          error
    ErrInvalidQuantity     error
)
```

**The transition table, exactly.** Absent edges are absent on purpose.

```go
var transitions = map[Status][]Status{
	StatusReserved: {StatusReleased, StatusCommitted, StatusExpired},
}
```

`released`, `committed` and `expired` are terminal: they are absent as keys, so
`CanTransitionTo` returns false for every edge out of them. **`committed` has no
edge to `released`.** Committed stock has been sold; releasing it would return
units to `available_quantity` that a customer has already bought. This mirrors
`awaiting_reconciliation` never reaching `cancelled` in the order domain.

- [ ] **Step 1: Write the failing tests**

Table-driven, mirroring `services/order/domain/order_test.go`. Read that file
first and match its shape.

Legal edges to assert: `reserved -> released`, `reserved -> committed`,
`reserved -> expired`.

Illegal edges to assert: `committed -> released`, `committed -> expired`,
`released -> reserved`, `released -> committed`, `expired -> reserved`,
`expired -> released`, `reserved -> reserved`.

Plus these named tests:
- `TestCommittedReservationCanNeverBeReleased` — its own test, not a table row,
  with a failure message saying committed stock has been sold.
- `TestTransitionToRejectsIllegalEdge` — asserts `r.Status` is unchanged after a
  rejected transition.
- `TestValidate` — table-driven over: valid; empty order id; empty sku; empty
  idempotency key; zero quantity; negative quantity. Each varies exactly one field.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./services/inventory/domain/...`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement**

Write `errors.go` and `reservation.go` against the interfaces above. `TransitionTo`
checks before it mutates, so a rejected edge leaves the struct untouched by
construction rather than by care.

- [ ] **Step 4: Run to verify they pass**

Run: `go test ./services/inventory/domain/... -v`

- [ ] **Step 5: Prove the package is pure**

Run: `go list -deps ./services/inventory/domain | grep -vE '^(internal/|[a-z/]+$)'`
Expected: no output. No driver, no `net/http`, no OpenTelemetry.

- [ ] **Step 6: Commit**

```bash
git add services/inventory/domain
git commit -m "feat(inventory): reservation state machine as pure functions

committed has no edge to released: committed stock has been sold, and returning
those units to available quantity would oversell what a customer already bought."
```

---

### Task 2: migrations and the two config fields

**Closes:** PL-37.

**Files:**
- Create: `services/inventory/migrations/00001_inventory.sql`, `00002_infrastructure.sql`, `migrations.go`
- Create: `services/inventory/migrations/migrations_test.go` (`//go:build integration`)
- Modify: `internal/config/config.go` and `config_test.go`

**Interfaces produced:**

```go
// services/inventory/migrations
func FS() fs.FS

// internal/config — two fields added to the existing Config struct
ReservationTTL           time.Duration   // RESERVATION_TTL, default 15m
ReservationSweepInterval time.Duration   // RESERVATION_SWEEP_INTERVAL, default 30s
```

Both go through the existing `parseDuration` helper, so a malformed value fails
at startup naming the variable. Add assertions for both defaults to
`TestLoadAppliesDefaults` — Epic A shipped that test asserting four of six
fields, and the two it missed were the two nobody thought about.

- [ ] **Step 1: Write `00001_inventory.sql`**

```sql
-- +goose Up
CREATE TABLE inventory_items (
    sku                text        PRIMARY KEY,
    available_quantity integer     NOT NULL CHECK (available_quantity >= 0),
    reserved_quantity  integer     NOT NULL DEFAULT 0 CHECK (reserved_quantity >= 0),
    version            bigint      NOT NULL DEFAULT 0,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE inventory_reservations (
    id              text        PRIMARY KEY,
    order_id        text        NOT NULL,
    sku             text        NOT NULL REFERENCES inventory_items (sku),
    quantity        integer     NOT NULL CHECK (quantity > 0),
    status          text        NOT NULL,
    expires_at      timestamptz NOT NULL,
    idempotency_key text        NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT inventory_reservations_status_valid
        CHECK (status IN ('reserved', 'released', 'committed', 'expired'))
);

CREATE INDEX inventory_reservations_sweepable
    ON inventory_reservations (expires_at)
    WHERE status = 'reserved';

-- +goose Down
DROP TABLE inventory_reservations;
DROP TABLE inventory_items;
```

The `CHECK (available_quantity >= 0)` is the second line of defence. The first is
the `WHERE` clause in Task 4's `UPDATE`. If the CHECK ever fires in practice, the
guard has been removed and Task 5's test should have caught it.

`UNIQUE (idempotency_key)` is how a repeated reservation is deduplicated. There is
no `internal/idempotency` involvement in this service.

The partial index matches the sweeper's query in Task 9 exactly, so the sweep does
not scan committed and released rows.

- [ ] **Step 2: Write `00002_infrastructure.sql`**

`outbox_events`, copied verbatim from
`services/order/migrations/00002_infrastructure.sql` minus the
`idempotency_keys` table, which this service does not use. Inventory publishes
events in this epic but consumes none, so it needs no inbox dedupe table;
Epic E adds `processed_events` when the Kafka consumers land. Read that file and
copy; do not retype from memory. The duplication across services is intended:
four databases, four migration histories.

- [ ] **Step 3: Write `migrations.go`**

`//go:embed *.sql` into an `embed.FS`, exposed as `func FS() fs.FS`. The package
sits in the same directory as the `.sql` files, so the FS is flat and no
`fs.Sub` is needed — that is what `postgres.Migrate` expects.

- [ ] **Step 4: Add the two config fields**

Follow the existing pattern in `internal/config/config.go` exactly. Read it first.

- [ ] **Step 5: Write the failing tests**

`internal/config/config_test.go`: extend `TestLoadAppliesDefaults` with assertions
for `ReservationTTL == 15*time.Minute` and `ReservationSweepInterval == 30*time.Second`,
and extend `TestLoadReadsOverrides` with both.

`services/inventory/migrations/migrations_test.go` (`//go:build integration`):
- `TestMigrationsApply` — `postgres.Migrate` against `postgres.StartPostgres`,
  then assert `inventory_items` and `inventory_reservations` exist via
  `information_schema.tables`.
- `TestNegativeStockIsRejected` — insert an item, then
  `UPDATE inventory_items SET available_quantity = -1` and assert it errors.
  This proves the CHECK is real rather than decorative.
- `TestDuplicateIdempotencyKeyIsRejected` — insert two reservations with the same
  key and assert the second violates the unique constraint.

- [ ] **Step 6: Run RED, implement, run GREEN**

Run: `go test ./internal/config/... && go test -tags=integration ./services/inventory/migrations/...`

- [ ] **Step 7: Commit**

```bash
git add services/inventory/migrations internal/config
git commit -m "feat(inventory): schema, and reservation TTL configuration

available_quantity and reserved_quantity carry CHECK constraints as a second
line of defence behind the reservation UPDATE's WHERE clause. idempotency_key is
unique: this service deduplicates on its own column rather than through the
shared middleware."
```

---

### Task 3: app ports and the use cases

**Closes:** PL-38.

**Files:**
- Create: `services/inventory/app/ports.go`, `app/reserve.go`, `app/manage.go`
- Create: `services/inventory/app/reserve_test.go`, `app/manage_test.go`

**Interfaces produced.** Ports are declared here, consumer-side, and implemented
in `adapter/`. This is the shape `services/order/app/ports.go` established; read
it first.

```go
type InventoryStore interface {
    // Reserve writes the reservation, decrements available stock and appends the
    // events in one transaction. Returns domain.ErrInsufficientStock when the
    // stock guard rejects the decrement.
    Reserve(ctx context.Context, r domain.Reservation, events []outbox.Event) error

    // ReservationByKey returns the reservation carrying the idempotency key, or
    // domain.ErrReservationNotFound.
    ReservationByKey(ctx context.Context, key string) (domain.Reservation, error)

    // Commit moves quantity out of reserved_quantity. Release returns it to
    // available_quantity. Both return domain.ErrIllegalTransition when the
    // reservation is not in the reserved state.
    Commit(ctx context.Context, id string, events []outbox.Event) (domain.Reservation, error)
    Release(ctx context.Context, id string, events []outbox.Event) (domain.Reservation, error)

    SetStock(ctx context.Context, sku string, quantity int) (domain.Item, error)

    // AppendEvents writes events in their own transaction. Used only for
    // InventoryReservationFailed: insufficient stock changes no state, so the
    // event has nothing to be atomic with, and Reserve's transaction has
    // already rolled back by the time the caller knows.
    AppendEvents(ctx context.Context, events []outbox.Event) error

    // There is deliberately no GetReservation. No endpoint reads a reservation
    // by id, and Commit and Release resolve their own ambiguity internally.

    // ExpireDue expires reservations past expires_at, up to limit, in one
    // transaction. buildEvent is called per claimed reservation so event shape
    // stays in app rather than the adapter.
    ExpireDue(ctx context.Context, limit int, buildEvent func(domain.Reservation) (outbox.Event, error)) (int, error)
}

type Reserve struct{ ... }
func NewReserve(store InventoryStore, ttl time.Duration, newID func() string) *Reserve
func (uc *Reserve) Execute(ctx context.Context, cmd ReserveCommand) (domain.Reservation, error)

type ReserveCommand struct {
    OrderID        string
    SKU            string
    Quantity       int
    IdempotencyKey string
}

type Manage struct{ ... }
func NewManage(store InventoryStore) *Manage
func (uc *Manage) Commit(ctx context.Context, id string) (domain.Reservation, error)
func (uc *Manage) Release(ctx context.Context, id string) (domain.Reservation, error)
func (uc *Manage) SetStock(ctx context.Context, sku string, quantity int) (domain.Item, error)
```

**Reserve's order of operations, exactly:**

1. `domain.Validate`. Return its error unchanged.
2. `ReservationByKey`. If found, return that reservation and nil error — the
   request is a repeat and reserves nothing. If `ErrReservationNotFound`, continue.
3. Build the reservation: `newID()`, status `reserved`, `ExpiresAt = now + ttl`.
4. Build the `InventoryReserved` event.
5. `store.Reserve`. On `domain.ErrInsufficientStock`, call
   `store.AppendEvents` with an `InventoryReservationFailed` event and return the
   sentinel — insufficient stock is a business outcome, not a failure of the
   service.

   The failure event needs its own transaction. `Reserve`'s transaction rolled
   back when the stock guard rejected the update, so nothing written inside it
   survives — and there is no state change for the event to be atomic with
   anyway. Do not try to write it inside `Reserve`.

Step 2 is a check-then-act with a race: two concurrent requests carrying the same
key can both miss. The `UNIQUE (idempotency_key)` constraint is what actually
enforces it — `store.Reserve` returns a unique-violation, and `Reserve` re-reads
by key and returns the winner's reservation. **Implement that recovery path**, and
test it. The lookup is an optimisation; the constraint is the guarantee.

**Event types:** `InventoryReserved`, `InventoryReservationFailed`,
`InventoryReservationCommitted`, `InventoryReservationReleased`,
`InventoryReservationExpired`. Aggregate type `reservation`, aggregate id the
reservation id. Build them with `outbox.NewEvent`.

- [ ] **Step 1: Write the failing tests**

Hand-written fakes implementing `InventoryStore`, in the shape of
`services/order/app/create_order_test.go`. No generated mocks.

`reserve_test.go`:
- reserves and returns a reservation in `reserved` status with `ExpiresAt` set from the TTL
- emits exactly one `InventoryReserved` event carrying the reservation id
- a repeat with a known key returns the existing reservation and calls `Reserve` zero times
- a unique-violation from the store causes a re-read by key and returns the winner
- insufficient stock returns `domain.ErrInsufficientStock` and emits `InventoryReservationFailed`
- each `domain.Validate` failure returns its sentinel and never touches the store

`manage_test.go`:
- commit and release return the updated reservation and emit their events
- `ErrIllegalTransition` from the store surfaces unchanged
- `SetStock` passes through and returns the item

- [ ] **Step 2: Run RED, implement, run GREEN**

Run: `go test ./services/inventory/app/... -race -v`

- [ ] **Step 3: Commit**

```bash
git add services/inventory/app
git commit -m "feat(inventory): reserve, commit, release and set-stock use cases

Reserve looks up the idempotency key first as an optimisation, but the unique
constraint is what enforces it: a concurrent repeat surfaces as a unique
violation and is recovered by re-reading the winner."
```

---

### Task 4: the Postgres adapter and its atomic reservation

**Closes:** PL-39.

**Files:**
- Create: `services/inventory/adapter/postgres/store.go`
- Create: `services/inventory/adapter/postgres/store_test.go` (`//go:build integration`)

This task carries the epic's teaching artifact. These statements are exact.

**The reservation.** One statement. No `SELECT` first, no `FOR UPDATE`, no
application-level lock:

```sql
UPDATE inventory_items
   SET available_quantity = available_quantity - $2,
       reserved_quantity  = reserved_quantity  + $2,
       version            = version + 1,
       updated_at         = now()
 WHERE sku = $1
   AND available_quantity >= $2
```

`RowsAffected() == 0` means insufficient stock — return `domain.ErrInsufficientStock`.
It does **not** mean an error occurred. The reservation row insert and the outbox
append join the same `postgres.WithTx`.

**Commit** applies the same idea to the reservation's own status, guarding the
transition in the `WHERE` clause rather than reading and checking in Go:

```sql
UPDATE inventory_reservations
   SET status = 'committed', updated_at = now()
 WHERE id = $1 AND status = 'reserved'
```

then, in the same transaction:

```sql
UPDATE inventory_items
   SET reserved_quantity = reserved_quantity - $2,
       version           = version + 1,
       updated_at        = now()
 WHERE sku = $1
```

**Release** is the same shape, with `status = 'released'` and:

```sql
UPDATE inventory_items
   SET available_quantity = available_quantity + $2,
       reserved_quantity  = reserved_quantity  - $2,
       version            = version + 1,
       updated_at         = now()
 WHERE sku = $1
```

For both, `RowsAffected() == 0` on the reservation update is ambiguous: the row
may not exist, or it may exist in a non-`reserved` status. Read it to distinguish,
and return `domain.ErrReservationNotFound` or `domain.ErrIllegalTransition`
accordingly. **Release must therefore refuse a `committed` reservation**, which
is the Task 1 invariant enforced a second time, in SQL.

**AppendEvents** is one `postgres.WithTx` around `outbox.Append`. It exists for
the insufficient-stock event, which has no accompanying state change.

**SetStock** upserts an absolute quantity:

```sql
INSERT INTO inventory_items (sku, available_quantity)
VALUES ($1, $2)
ON CONFLICT (sku) DO UPDATE
   SET available_quantity = EXCLUDED.available_quantity,
       version            = inventory_items.version + 1,
       updated_at         = now()
RETURNING sku, available_quantity, reserved_quantity, version
```

**ExpireDue** claims with `FOR UPDATE SKIP LOCKED`, matching the outbox
publisher's shape so replicas take disjoint batches:

```sql
SELECT id, order_id, sku, quantity, status, expires_at, idempotency_key
  FROM inventory_reservations
 WHERE status = 'reserved'
   AND expires_at <= now()
 ORDER BY expires_at
 FOR UPDATE SKIP LOCKED
 LIMIT $1
```

`status = 'reserved'` is load-bearing: a committed reservation past its
`expires_at` must never be swept.

- [ ] **Step 1: Write the failing integration tests**

`//go:build integration`, using `postgres.StartPostgres` and
`postgres.Migrate(ctx, pool, migrations.FS())`.

- reserve succeeds, decrements available, increments reserved, writes the outbox row
- reserve for more than available returns `domain.ErrInsufficientStock` and changes nothing
- **reserve rolls back entirely when the outbox append fails** — pass a deliberately
  malformed event and assert stock is unchanged and no reservation row exists.
  This is the Epic A atomicity test applied here, and it is the one that proves
  the transaction is real.
- commit moves quantity out of reserved and leaves available alone
- release returns quantity to available
- **release on a committed reservation returns `domain.ErrIllegalTransition`** and
  changes no stock
- commit or release on an unknown id returns `domain.ErrReservationNotFound`
- `SetStock` creates then overwrites, and the second call does not accumulate
- `ExpireDue` expires a due reserved reservation and leaves a committed one alone

- [ ] **Step 2: Run RED, implement, run GREEN**

Run: `go test -tags=integration -race ./services/inventory/adapter/postgres/... -v`

- [ ] **Step 3: Commit**

```bash
git add services/inventory/adapter/postgres
git commit -m "feat(inventory): atomic reservation in a single statement

The stock guard is the UPDATE's WHERE clause, so no read-then-write window
exists. Zero rows affected means insufficient stock, which is a business
outcome. Commit and release guard their transition the same way."
```

---

### Task 5: the concurrency test, mutation-proven

**Closes:** PL-45.

**Files:**
- Create: `services/inventory/adapter/postgres/concurrency_test.go` (`//go:build integration`)

This is the most important test in the epic and it gets its own task so it gets
its own review.

**Interfaces:** consumes Task 4's store. Produces nothing.

- [ ] **Step 1: Write the test**

`TestConcurrentBuyersOfTheLastUnit`:
- seed one sku with `available_quantity = 1` via `SetStock`
- launch 16 goroutines, each reserving quantity 1 with its own distinct
  idempotency key. **Distinct keys matter**: identical keys would make the losers
  fail on deduplication rather than on stock, and the test would pass while
  proving nothing about overselling.
- collect outcomes; assert exactly one nil error and fifteen
  `domain.ErrInsufficientStock`
- assert `available_quantity == 0` and `reserved_quantity == 1`
- assert exactly one row in `inventory_reservations`

Run under `-race`.

- [ ] **Step 2: Run it and watch it pass**

Run: `go test -tags=integration -race -run TestConcurrentBuyersOfTheLastUnit ./services/inventory/adapter/postgres/... -v`

- [ ] **Step 3: Prove it discriminates — this task is not done without this**

Extract the commit into a scratch directory so the working tree is untouched:

```bash
T=$(mktemp -d) && git archive HEAD | tar -x -C "$T"
```

In `$T`, replace the single-statement reservation with a read-then-write:

```go
// SELECT available_quantity FROM inventory_items WHERE sku = $1
// if available >= quantity { UPDATE inventory_items SET available_quantity = available_quantity - $2 ... }
```

Rerun the test in `$T`. **It must FAIL** — more than one reservation succeeds, or
stock goes negative and the CHECK constraint fires. Paste the failing output into
your report, then `rm -rf "$T"`.

Epic A shipped a concurrency test that looked rigorous and discriminated nothing:
removing `FOR UPDATE SKIP LOCKED` from the outbox publisher left it green,
because plain `FOR UPDATE` blocks and then re-evaluates under READ COMMITTED.
That was only discovered by trying to break it. A test never seen failing is an
assumption.

If the mutated version *passes*, stop and report it. That would mean the
single-statement form is not what makes this work, and the epic's central claim
is wrong — which is worth knowing before it reaches a blog post.

- [ ] **Step 4: Commit**

```bash
git add services/inventory/adapter/postgres/concurrency_test.go
git commit -m "test(inventory): sixteen buyers, one unit, exactly one winner

Verified to discriminate: replacing the single-statement reservation with a
read-then-write makes this test fail."
```

---

### Task 6: the HTTP adapter — all four endpoints

**Closes:** PL-40, PL-41, PL-42.

Three stories, one task: all four handlers live in one file and share the error
mapping and the route registration. Splitting them would mean three reviews of
the same two files.

**Files:**
- Create: `services/inventory/adapter/http/handler.go`, `routes.go`
- Create: `services/inventory/adapter/http/handler_test.go`
- Create: `services/inventory/adapter/http/routes_integration_test.go` (`//go:build integration`)

**Interfaces produced:**

```go
func NewHandler(reserve *app.Reserve, manage *app.Manage) *Handler
func (h *Handler) SetStock(w http.ResponseWriter, r *http.Request)     // PUT /inventory/items/{sku}
func (h *Handler) Reserve(w http.ResponseWriter, r *http.Request)      // POST /inventory/reservations
func (h *Handler) Commit(w http.ResponseWriter, r *http.Request)       // POST /inventory/reservations/{id}/commit
func (h *Handler) Release(w http.ResponseWriter, r *http.Request)      // POST /inventory/reservations/{id}/release
func Register(mux *http.ServeMux, h *Handler)
```

`Register` uses `httpx.Route` for every route, so spans are named after the route
pattern. There is no idempotency middleware on this service.

**The wire contract, exactly.**

`PUT /inventory/items/{sku}` body `{"available_quantity": N}` → `200` with
`{"sku","available_quantity","reserved_quantity","version"}`. Negative N → `400`
`invalid_request`. Absolute, not additive: a repeat call sets the same value, which
is what makes a scenario rerunnable.

`POST /inventory/reservations` body
`{"order_id","sku","quantity","idempotency_key"}` → `201` with
`{"reservation_id","order_id","sku","quantity","status","expires_at"}`.
A repeat with a known key → `200` with the existing reservation, not `201`. The
different status code is how a caller can tell a reservation happened from a
replay.

`POST /inventory/reservations/{id}/commit` and `/release` → `200` with the updated
reservation.

**Error mapping**, exhaustive, with a `500` default that never echoes an internal
error string:

| Sentinel | Status | Code |
|---|---|---|
| `domain.ErrMissingOrderID`, `ErrMissingSKU`, `ErrMissingKey`, `ErrInvalidQuantity` | 400 | `invalid_request` |
| malformed JSON, unknown field | 400 | `invalid_request` |
| `domain.ErrInsufficientStock` | 409 | `insufficient_stock` |
| `domain.ErrIllegalTransition` | 409 | `illegal_transition` |
| `domain.ErrReservationNotFound` | 404 | `reservation_not_found` |
| `domain.ErrItemNotFound` | 404 | `item_not_found` |

The `insufficient_stock` response carries `details` naming the sku and the
requested quantity, so a caller learns what it could not have.

Decoders set `DisallowUnknownFields`.

- [ ] **Step 1: Write the failing handler tests**

Fakes for `*app.Reserve` and `*app.Manage` are not possible — they are structs,
not interfaces. Instead build them over a fake `app.InventoryStore`, exactly as
`services/order/adapter/http/handler_test.go` builds its handler over a stub
store. Read that file first.

Register on a **real `http.ServeMux`** via `Register`, not by calling handler
functions directly. `r.PathValue("sku")` and `r.PathValue("id")` are only
populated by ServeMux routing; calling the function directly leaves them empty
and the test passes for the wrong reason.

Cover: each success shape; each row of the error table; the `201` versus `200`
distinction on a repeated key; a negative quantity on `SetStock`; an unknown
field rejected.

- [ ] **Step 2: Write the failing integration test**

`routes_integration_test.go`, over a real Postgres and the real store, driving
through `Register`:
1. `PUT` one unit of a sku
2. `POST` a reservation — expect `201`
3. the identical `POST` — expect `200` and the same reservation id
4. `POST` a second reservation for the same unit with a fresh key — expect `409` `insufficient_stock`
5. commit the first — expect `200` and status `committed`
6. release it — expect `409` `illegal_transition`

Step 6 is the invariant end to end: a committed reservation cannot be released
through the API, not just through the domain.

Epic A shipped a whole epic with `routes.Register` exercised by no test, and the
bug that hid there cost a whole-branch review to find. This test is that lesson.

- [ ] **Step 3: Run RED, implement, run GREEN**

Run: `go test ./services/inventory/adapter/http/... && go test -tags=integration -race ./services/inventory/adapter/http/...`

- [ ] **Step 4: Commit**

```bash
git add services/inventory/adapter/http
git commit -m "feat(inventory): stock, reservation, commit and release endpoints

A repeated reservation returns 200 rather than 201, so a caller can tell a new
reservation from a replay. Release refuses a committed reservation at the API
boundary as well as in the domain."
```

---

### Task 7: the expiry sweeper

**Closes:** PL-43.

**Files:**
- Create: `services/inventory/app/sweeper.go`, `app/sweeper_test.go` (`//go:build integration`)

**Interfaces produced:**

```go
type Sweeper struct{ ... }
func NewSweeper(store InventoryStore, logger *slog.Logger, opts SweeperOptions) *Sweeper
func (s *Sweeper) Run(ctx context.Context) error

type SweeperOptions struct {
    Interval  time.Duration
    BatchSize int
}
```

`Run` ticks on `Interval`, calls `store.ExpireDue` with `BatchSize`, logs how many
it expired when non-zero, and returns `nil` on context cancellation. Model it on
`outbox.Publisher.Run` — read that file; the shape is established and the
errgroup in the composition root depends on `nil`-on-cancel.

Reserved stock that nobody commits must come back, or a caller that crashes
between reserving and paying leaks inventory permanently.

- [ ] **Step 1: Write the failing tests**

`//go:build integration`, real Postgres:
- reserve with a TTL already in the past, run one sweep, assert the reservation is
  `expired` and `available_quantity` came back
- commit a reservation, force its `expires_at` into the past, sweep, and assert it
  is **still `committed`** and stock did not move. A committed reservation is
  never swept, whatever the clock says.
- an `InventoryReservationExpired` event is written for each expiry
- `Run` returns `nil` promptly after context cancellation

- [ ] **Step 2: Run RED, implement, run GREEN**

Run: `go test -tags=integration -race ./services/inventory/app/... -v`

- [ ] **Step 3: Commit**

```bash
git add services/inventory/app/sweeper.go services/inventory/app/sweeper_test.go
git commit -m "feat(inventory): expire reservations nobody committed

Claims with FOR UPDATE SKIP LOCKED so replicas take disjoint batches. Only
reserved rows are claimed, so a committed reservation past its expiry is left
alone."
```

---

### Task 8: cmd/inventory, Compose and the scrape target

**Closes:** PL-44.

**Files:**
- Create: `cmd/inventory/main.go`
- Modify: `deploy/docker-compose.yml`, `deploy/prometheus.yml`

**Consumes:** everything above, plus every `internal/` package.

`cmd/inventory/main.go` mirrors `cmd/order/main.go`. Read it and follow it exactly:
config, logger, `slog.SetDefault`, otelx with deferred flush, pool, **migrations
before the server listens**, routes, then the HTTP server, the outbox publisher
and the sweeper under one `errgroup`.

`/healthz`, `/readyz` and `/metrics` as in the order service — `/metrics` via
`mux.Handle`, not `httpx.Route`.

**Compose:** a new `inventory` service built from the existing
`deploy/Dockerfile` with `args: SERVICE: inventory`. **No new Dockerfile, and no
edit to the existing one.** If you find yourself changing it, stop and report —
`ARG SERVICE` already does this. Host port `8081`. `DATABASE_DSN` points at the
`inventory` database, which `deploy/postgres/init.sql` already creates. Same
`depends_on` conditions as `order`.

**Prometheus:** add an `inventory` scrape target alongside `order`.

- [ ] **Step 1: Implement and bring the stack up**

Run `make docker-up`, then verify against the running stack and paste the real
output:

```bash
curl -s localhost:8081/healthz
curl -s localhost:8081/readyz
curl -s localhost:8081/metrics | head -3
curl -s 'http://localhost:9090/api/v1/targets' | grep -o '"health":"[a-z]*"'
curl -s 'http://localhost:16686/api/services'
```

Expect both Prometheus targets `up`, and `inventory` in Jaeger's service list
after you issue one request to it.

- [ ] **Step 2: Exercise it by hand**

```bash
curl -is -X PUT localhost:8081/inventory/items/playstation-5 -H 'Content-Type: application/json' -d '{"available_quantity":1}'
curl -is -X POST localhost:8081/inventory/reservations -H 'Content-Type: application/json' \
  -d '{"order_id":"ord_1","sku":"playstation-5","quantity":1,"idempotency_key":"k1"}'
curl -is -X POST localhost:8081/inventory/reservations -H 'Content-Type: application/json' \
  -d '{"order_id":"ord_2","sku":"playstation-5","quantity":1,"idempotency_key":"k2"}'
```

Expect `200`, `201`, then `409 insufficient_stock`. Paste all three.

- [ ] **Step 3: `make docker-down`, then lint, race, integration, clean checkout**

- [ ] **Step 4: Commit**

```bash
git add cmd/inventory deploy
git commit -m "feat(inventory): service binary, compose service and scrape target

Built from the existing Dockerfile via ARG SERVICE; no second Dockerfile."
```

---

### Task 9: labctl scenario last-playstation

**Closes:** PL-46.

**Files:**
- Create: `cmd/labctl/last_playstation.go`
- Modify: `cmd/labctl/main.go`, `cmd/labctl/scenario.go`, `cmd/labctl/client.go`
- Modify: `.github/workflows/ci.yml`

**Three changes to existing labctl code:**

1. `environment` gains `inventoryBaseURL`, and `main.go` gains an `-inventory-url`
   flag defaulting to `http://localhost:8081`, following the `-order-url` pattern.
2. `scenario` gains a `service string` field naming the service whose traces the
   run should link to. `main.go` currently hardcodes `"order"` in
   `ran.traceURL(env.jaegerURL, "order")`; take it from the scenario instead, and
   set `service: "order"` on `duplicate-order`.
3. A client for the inventory API, in the shape of the existing `orderClient`.
   Close each response body per call, not with a `defer` inside a loop.

**The scenario:**

1. `waitReady` against the inventory service.
2. `PUT /inventory/items/playstation-5` with `available_quantity: 1`.
3. Fire N=16 concurrent reservations, **each with its own idempotency key**, so
   the losers lose on stock rather than on deduplication.
4. Assert exactly one `201` and fifteen `409`, and that every `409` carries code
   `insufficient_stock` rather than some other conflict.
5. Report the winning reservation id and the losing status code, so the output
   shows why the losers lost.
6. Rerunnable: step 2 sets an absolute quantity, so a second run passes.

The trace URL it prints must carry the run's time window, as `duplicate-order`
now does. A URL without `start` and `end` is rejected by Jaeger v2's v3 API with
`query.startTimeMin and query.startTimeMax are required`.

- [ ] **Step 1: Implement, then run it against the live stack**

Run: `make docker-up && make scenario NAME=last-playstation`
Expect `PASS`, the winner's id, and a trace URL.

- [ ] **Step 2: Open the printed trace URL in a browser and confirm it renders**

The URL must produce no console errors and show traces. Report how you checked.

- [ ] **Step 3: Prove the scenario can fail**

Break the atomic reservation in the running stack — replace it with a
read-then-write — rebuild, rerun. Expect `FAIL` and a non-zero exit. Restore, rerun,
expect `PASS`. Report all three runs and confirm the working tree is clean
afterwards.

- [ ] **Step 4: Add it to the CI scenario job**

The `scenario` job is `main`-only. Add `last-playstation` alongside
`duplicate-order`.

- [ ] **Step 5: `make docker-down`, verify, commit**

---

### Task 10: the post

**Closes:** PL-47. Closes the epic.

**Files:**
- Create: `docs/content/posts/the-last-playstation-problem.md`

Write it from what actually happened building Tasks 1 through 9, not from this
plan. Where the implementation diverged, say so — those corrections are the
series.

**Structure:** the seven fixed headings as literal `##` sections — Problem,
Decision, Tradeoff, Failure mode, How I tested it, How I observed it, Run it
yourself.

**The spine of the post** is the contrast from Task 5: the read-then-write
version overselling, with real captured output, against the single-statement
version holding. Show both. That contrast is the article; everything else is
supporting material.

**Tradeoff, honestly:** no distributed lock means no coordination cost and no
lock-service dependency, but the guarantee now lives inside one SQL statement
that a refactor could quietly break — which is exactly why Task 5 is
mutation-proven rather than merely green.

**Failure mode:** reserved stock leaks if nobody commits, which is what the
sweeper exists for. And a reservation whose TTL lapses while a payment outcome is
still unknown is a real hole this epic does not close — Epic D confronts it.

**Do not claim:** that a trace shows database timing. `internal/postgres` still
emits no spans; per-query tracing is Epic G. There is no saga, no broker, and the
order service does not call inventory in this epic.

**Run it yourself:** `make scenario NAME=last-playstation` with real output, and
trace output fetched from Jaeger's API rather than a screenshot.

- [ ] **Step 1: Write it**
- [ ] **Step 2: Build the site and confirm it renders under Posts with all seven headings**
- [ ] **Step 3: Read it end to end as prose, out of the diff**

Check for sentences left malformed by an edit, and for claims elsewhere in the
post that an edit made false. Epic A shipped a truncated sentence that three
readers missed because all three read it as a diff.

- [ ] **Step 4: Full verification and commit**

```bash
make lint && make test-race && make integration
make docker-up && make scenario NAME=duplicate-order && make scenario NAME=last-playstation && make docker-down
```

---

## Definition of done for Epic B

- `make lint`, `make test-race` and `make integration` pass.
- Both scenarios pass against a live stack, and `last-playstation` has been seen failing.
- The concurrency test has been seen failing against a read-then-write reservation.
- Both Prometheus targets are up; `inventory` appears in Jaeger.
- `services/inventory/domain` imports only the standard library.
- `deploy/Dockerfile` is unchanged.
- The post is live with real captured output.
