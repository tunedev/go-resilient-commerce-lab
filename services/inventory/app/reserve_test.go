package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

// byKeyResult is one queued response for a ReservationByKey call.
type byKeyResult struct {
	reservation domain.Reservation
	err         error
}

// fakeStore is a hand-written InventoryStore for the use case tests. Each
// method records what it was called with and returns whatever the test
// configured.
type fakeStore struct {
	byKeyResults []byKeyResult
	byKeyCalls   int

	reserveErr   error
	reserveCalls int
	reservation  domain.Reservation
	reserveEvent []outbox.Event

	appendedEvents []outbox.Event
	appendErr      error

	commitResult domain.Reservation
	commitErr    error
	commitEvents []outbox.Event
	commitID     string

	releaseResult domain.Reservation
	releaseErr    error
	releaseEvents []outbox.Event
	releaseID     string

	setStockItem domain.Item
	setStockErr  error
	setStockSKU  string
	setStockQty  int
}

func (f *fakeStore) Reserve(_ context.Context, r domain.Reservation, events []outbox.Event) error {
	f.reserveCalls++
	f.reservation = r
	f.reserveEvent = events
	return f.reserveErr
}

func (f *fakeStore) ReservationByKey(_ context.Context, _ string) (domain.Reservation, error) {
	if f.byKeyCalls >= len(f.byKeyResults) {
		return domain.Reservation{}, domain.ErrReservationNotFound
	}
	result := f.byKeyResults[f.byKeyCalls]
	f.byKeyCalls++
	return result.reservation, result.err
}

func (f *fakeStore) Commit(_ context.Context, id string, events []outbox.Event) (domain.Reservation, error) {
	f.commitID = id
	f.commitEvents = events
	return f.commitResult, f.commitErr
}

func (f *fakeStore) Release(_ context.Context, id string, events []outbox.Event) (domain.Reservation, error) {
	f.releaseID = id
	f.releaseEvents = events
	return f.releaseResult, f.releaseErr
}

func (f *fakeStore) SetStock(_ context.Context, sku string, quantity int) (domain.Item, error) {
	f.setStockSKU = sku
	f.setStockQty = quantity
	return f.setStockItem, f.setStockErr
}

func (f *fakeStore) AppendEvents(_ context.Context, events []outbox.Event) error {
	f.appendedEvents = events
	return f.appendErr
}

func (f *fakeStore) ExpireDue(_ context.Context, _ int, _ func(domain.Reservation) (outbox.Event, error)) (int, error) {
	return 0, nil
}

func newReserve(store app.InventoryStore) *app.Reserve {
	return app.NewReserve(store, time.Hour, func() string { return "resv_test" })
}

func validReserveCommand() app.ReserveCommand {
	return app.ReserveCommand{
		OrderID:        "order_1",
		SKU:            "playstation-5",
		Quantity:       2,
		IdempotencyKey: "key_1",
	}
}

func TestReserveReturnsAReservedReservationWithExpiryFromTTL(t *testing.T) {
	store := &fakeStore{}
	before := time.Now()

	r, err := newReserve(store).Execute(context.Background(), validReserveCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if r.ID != "resv_test" {
		t.Errorf("ID = %q, want resv_test", r.ID)
	}
	if r.Status != domain.StatusReserved {
		t.Errorf("Status = %q, want %q", r.Status, domain.StatusReserved)
	}
	if r.ExpiresAt.Before(before.Add(time.Hour)) {
		t.Errorf("ExpiresAt = %v, want at or after %v", r.ExpiresAt, before.Add(time.Hour))
	}
}

func TestReserveEmitsOneInventoryReservedEventCarryingTheReservationID(t *testing.T) {
	store := &fakeStore{}

	r, err := newReserve(store).Execute(context.Background(), validReserveCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(store.reserveEvent) != 1 {
		t.Fatalf("emitted %d events, want 1", len(store.reserveEvent))
	}
	e := store.reserveEvent[0]
	if e.EventType != "InventoryReserved" {
		t.Errorf("EventType = %q", e.EventType)
	}
	if e.AggregateType != "reservation" || e.AggregateID != r.ID {
		t.Errorf("aggregate = %s/%s, want reservation/%s", e.AggregateType, e.AggregateID, r.ID)
	}
}

func TestReserveWithKnownKeyReturnsExistingReservationWithoutCallingReserve(t *testing.T) {
	existing := domain.Reservation{ID: "resv_existing", Status: domain.StatusReserved}
	store := &fakeStore{byKeyResults: []byKeyResult{{reservation: existing}}}

	r, err := newReserve(store).Execute(context.Background(), validReserveCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if r != existing {
		t.Errorf("r = %+v, want %+v", r, existing)
	}
	if store.reserveCalls != 0 {
		t.Errorf("Reserve called %d times, want 0", store.reserveCalls)
	}
}

func TestReserveRecoversFromDuplicateKeyByReReadingTheWinner(t *testing.T) {
	winner := domain.Reservation{ID: "resv_winner", Status: domain.StatusReserved}
	store := &fakeStore{
		byKeyResults: []byKeyResult{
			{err: domain.ErrReservationNotFound},
			{reservation: winner},
		},
		reserveErr: app.ErrDuplicateKey,
	}

	r, err := newReserve(store).Execute(context.Background(), validReserveCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if r != winner {
		t.Errorf("r = %+v, want winner %+v", r, winner)
	}
	if store.reserveCalls != 1 {
		t.Errorf("Reserve called %d times, want 1", store.reserveCalls)
	}
	if store.byKeyCalls != 2 {
		t.Errorf("ReservationByKey called %d times, want 2", store.byKeyCalls)
	}
}

func TestReserveInsufficientStockEmitsFailureEventAndReturnsSentinel(t *testing.T) {
	store := &fakeStore{reserveErr: domain.ErrInsufficientStock}

	_, err := newReserve(store).Execute(context.Background(), validReserveCommand())
	if !errors.Is(err, domain.ErrInsufficientStock) {
		t.Fatalf("err = %v, want ErrInsufficientStock", err)
	}

	if len(store.appendedEvents) != 1 {
		t.Fatalf("appended %d events, want 1", len(store.appendedEvents))
	}
	e := store.appendedEvents[0]
	if e.EventType != "InventoryReservationFailed" {
		t.Errorf("EventType = %q", e.EventType)
	}
	if e.AggregateType != "reservation" {
		t.Errorf("AggregateType = %q, want reservation", e.AggregateType)
	}
}

func TestReserveInsufficientStockSurfacesALookupFailure(t *testing.T) {
	lookupErr := errors.New("connection reset")
	store := &fakeStore{
		byKeyResults: []byKeyResult{
			{err: domain.ErrReservationNotFound},
			{err: lookupErr},
		},
		reserveErr: domain.ErrInsufficientStock,
	}

	_, err := newReserve(store).Execute(context.Background(), validReserveCommand())
	if !errors.Is(err, lookupErr) {
		t.Fatalf("err = %v, want the lookup error", err)
	}

	if len(store.appendedEvents) != 0 {
		t.Errorf("appended %d failure events, want 0: the stock verdict is unknown", len(store.appendedEvents))
	}
}

func TestReserveInsufficientStockReplaysWhenTheKeyWonTheStockRace(t *testing.T) {
	winner := domain.Reservation{ID: "resv_winner", Status: domain.StatusReserved}
	store := &fakeStore{
		byKeyResults: []byKeyResult{
			{err: domain.ErrReservationNotFound},
			{reservation: winner},
		},
		reserveErr: domain.ErrInsufficientStock,
	}

	r, err := newReserve(store).Execute(context.Background(), validReserveCommand())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if r != winner {
		t.Errorf("r = %+v, want winner %+v", r, winner)
	}
	if len(store.appendedEvents) != 0 {
		t.Errorf("appended %d failure events, want 0: a replay is not a failure", len(store.appendedEvents))
	}
}

func TestReserveRejectsInvalidCommandBeforeTouchingTheStore(t *testing.T) {
	cases := []struct {
		name string
		cmd  app.ReserveCommand
		want error
	}{
		{"no order id", app.ReserveCommand{SKU: "playstation-5", Quantity: 1, IdempotencyKey: "k"}, domain.ErrMissingOrderID},
		{"no sku", app.ReserveCommand{OrderID: "order_1", Quantity: 1, IdempotencyKey: "k"}, domain.ErrMissingSKU},
		{"no key", app.ReserveCommand{OrderID: "order_1", SKU: "playstation-5", Quantity: 1}, domain.ErrMissingKey},
		{"zero quantity", app.ReserveCommand{OrderID: "order_1", SKU: "playstation-5", IdempotencyKey: "k"}, domain.ErrInvalidQuantity},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			_, err := newReserve(store).Execute(context.Background(), tc.cmd)
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if store.reserveCalls != 0 || store.byKeyCalls != 0 {
				t.Error("the store was called for an invalid command")
			}
		})
	}
}
