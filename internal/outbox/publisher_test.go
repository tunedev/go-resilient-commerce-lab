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
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tunedev/go-resilient-commerce-lab/internal/logger"
	"github.com/tunedev/go-resilient-commerce-lab/internal/outbox"
	"github.com/tunedev/go-resilient-commerce-lab/internal/postgres"
)

type recordingSink struct {
	mu   sync.Mutex
	seen []outbox.Event
	fail error
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
