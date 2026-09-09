package httpx

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Health reports liveness. It checks nothing: a liveness probe that fails when
// a dependency is down turns a dependency outage into a restart loop.
func Health() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

// Ready reports readiness. A failing check means the instance should be taken
// out of rotation, not restarted.
func Ready(check func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := check(r.Context()); err != nil {
			WriteError(r.Context(), w, http.StatusServiceUnavailable,
				"not_ready", "a dependency is unavailable", nil)
			return
		}
		WriteJSON(r.Context(), w, http.StatusOK, map[string]string{"status": "ready"})
	})
}

// Metrics serves the Prometheus exposition for registry.
func Metrics(registry *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}
