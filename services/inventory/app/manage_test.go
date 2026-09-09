package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/app"
	"github.com/tunedev/go-resilient-commerce-lab/services/inventory/domain"
)

func TestManageCommitReturnsTheUpdatedReservationAndEmitsItsEvent(t *testing.T) {
	want := domain.Reservation{ID: "resv_1", Status: domain.StatusCommitted}
	store := &fakeStore{commitResult: want}

	got, err := app.NewManage(store).Commit(context.Background(), "resv_1")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got != want {
		t.Errorf("got = %+v, want %+v", got, want)
	}

	if store.commitID != "resv_1" {
		t.Errorf("Commit called with id %q, want resv_1", store.commitID)
	}
	if len(store.commitEvents) != 1 || store.commitEvents[0].EventType != "InventoryReservationCommitted" {
		t.Errorf("commitEvents = %+v, want one InventoryReservationCommitted event", store.commitEvents)
	}
}

func TestManageReleaseReturnsTheUpdatedReservationAndEmitsItsEvent(t *testing.T) {
	want := domain.Reservation{ID: "resv_1", Status: domain.StatusReleased}
	store := &fakeStore{releaseResult: want}

	got, err := app.NewManage(store).Release(context.Background(), "resv_1")
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got != want {
		t.Errorf("got = %+v, want %+v", got, want)
	}

	if store.releaseID != "resv_1" {
		t.Errorf("Release called with id %q, want resv_1", store.releaseID)
	}
	if len(store.releaseEvents) != 1 || store.releaseEvents[0].EventType != "InventoryReservationReleased" {
		t.Errorf("releaseEvents = %+v, want one InventoryReservationReleased event", store.releaseEvents)
	}
}

func TestManageCommitSurfacesIllegalTransitionUnchanged(t *testing.T) {
	store := &fakeStore{commitErr: domain.ErrIllegalTransition}

	_, err := app.NewManage(store).Commit(context.Background(), "resv_1")
	if !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
}

func TestManageReleaseSurfacesIllegalTransitionUnchanged(t *testing.T) {
	store := &fakeStore{releaseErr: domain.ErrIllegalTransition}

	_, err := app.NewManage(store).Release(context.Background(), "resv_1")
	if !errors.Is(err, domain.ErrIllegalTransition) {
		t.Fatalf("err = %v, want ErrIllegalTransition", err)
	}
}

func TestManageSetStockPassesThroughAndReturnsTheItem(t *testing.T) {
	want := domain.Item{SKU: "playstation-5", AvailableQuantity: 10}
	store := &fakeStore{setStockItem: want}

	got, err := app.NewManage(store).SetStock(context.Background(), "playstation-5", 10)
	if err != nil {
		t.Fatalf("SetStock: %v", err)
	}
	if got != want {
		t.Errorf("got = %+v, want %+v", got, want)
	}
	if store.setStockSKU != "playstation-5" || store.setStockQty != 10 {
		t.Errorf("SetStock called with (%q, %d)", store.setStockSKU, store.setStockQty)
	}
}
