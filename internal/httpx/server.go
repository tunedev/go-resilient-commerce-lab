package httpx

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// Options configures a Server.
type Options struct {
	Addr            string
	Handler         http.Handler
	Logger          *slog.Logger
	ShutdownTimeout time.Duration
}

// Server is an HTTP server with a fixed middleware chain and graceful shutdown.
type Server struct {
	httpServer      *http.Server
	logger          *slog.Logger
	shutdownTimeout time.Duration
}

// NewServer wraps the handler in the standard chain: tracing outermost, then
// request id, request logging, and panic recovery innermost. Recovery sits
// inside logging so a recovered panic's 500 still reaches the access log.
func NewServer(o Options) *Server {
	handler := RequestLogging(o.Logger)(Recovery(o.Logger)(o.Handler))
	handler = RequestID(handler)
	handler = otelhttp.NewHandler(handler, "http.server")

	return &Server{
		httpServer: &http.Server{
			Addr:              o.Addr,
			Handler:           handler,
			ReadHeaderTimeout: 5 * time.Second,
		},
		logger:          o.Logger,
		shutdownTimeout: o.ShutdownTimeout,
	}
}

// Run serves until ctx is cancelled, then drains in-flight requests within the
// shutdown timeout. It returns nil on a clean shutdown.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.InfoContext(ctx, "http server listening", slog.String("addr", s.httpServer.Addr))
		if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()

	s.logger.InfoContext(shutdownCtx, "http server shutting down")
	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return <-errCh
}
