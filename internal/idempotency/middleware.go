package idempotency

import (
	"bytes"
	"context"
	"io"
	"net/http"

	"github.com/tunedev/go-resilient-commerce-lab/internal/httpx"
)

const headerName = "Idempotency-Key"

const maxBodyBytes = 1 << 20

type contextKey int

const keyContextKey contextKey = iota

// KeyFromContext returns the idempotency key this request holds.
func KeyFromContext(ctx context.Context) (string, bool) {
	key, ok := ctx.Value(keyContextKey).(string)
	return key, ok
}

type statusCapture struct {
	http.ResponseWriter
	status int
}

func (s *statusCapture) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func (s *statusCapture) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// Require enforces the Idempotency-Key header. It claims the key before the
// handler runs and releases the claim if the handler fails, so a retry is not
// permanently blocked by a transient error.
func Require(store *Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			key := r.Header.Get(headerName)
			if key == "" {
				httpx.WriteError(ctx, w, http.StatusBadRequest,
					"idempotency_key_required", "the Idempotency-Key header is required", nil)
				return
			}

			body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
			if err != nil {
				httpx.WriteError(ctx, w, http.StatusBadRequest,
					"invalid_request", "could not read the request body", nil)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			hash, err := Hash(body)
			if err != nil {
				httpx.WriteError(ctx, w, http.StatusBadRequest,
					"invalid_request", "the request body is not valid JSON", nil)
				return
			}

			outcome, record, err := store.Claim(ctx, key, hash)
			if err != nil {
				httpx.WriteError(ctx, w, http.StatusInternalServerError,
					"internal_error", "internal error", nil)
				return
			}

			switch outcome {
			case OutcomeConflict:
				httpx.WriteError(ctx, w, http.StatusConflict,
					"idempotency_key_reused",
					"this Idempotency-Key was used with a different request body", nil)
				return
			case OutcomeInProgress:
				httpx.WriteJSON(ctx, w, http.StatusAccepted,
					map[string]string{"status": "processing"})
				return
			case OutcomeReplay:
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Idempotent-Replay", "true")
				w.WriteHeader(record.StatusCode)
				_, _ = w.Write(record.ResponseBody)
				return
			}

			capture := &statusCapture{ResponseWriter: w}
			handlerReturned := false

			// Deferred so the claim is released on a panic as well as on a
			// non-2xx. A validation failure records no result, so the client
			// can correct the request and retry under the same key.
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
		})
	}
}
