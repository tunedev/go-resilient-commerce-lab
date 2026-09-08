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
	httpx.Route(mux, "GET /healthz", httpx.Health())
	httpx.Route(mux, "GET /readyz", httpx.Ready(pool.Ping))
	// Handle, not Route: otelhttp still spans the scrape, but the span keeps
	// its default name instead of joining the route patterns.
	mux.Handle("GET /metrics", httpx.Metrics(providers.Registry))
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
}
