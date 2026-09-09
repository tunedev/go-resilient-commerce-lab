package httpx

import (
	"net/http"

	"go.opentelemetry.io/otel/trace"
)

// Route registers handler at pattern and renames the active server span to the
// route pattern. ServeMux does not tell an outer middleware which pattern
// matched, so the rename happens inside the registered handler, which closes
// over its own pattern, and the span name stays low cardinality.
//
// handler is an http.Handler so that per-route middleware composes without an
// adapter at every call site.
func Route(mux *http.ServeMux, pattern string, handler http.Handler) {
	mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trace.SpanFromContext(r.Context()).SetName(pattern)
		handler.ServeHTTP(w, r)
	}))
}
