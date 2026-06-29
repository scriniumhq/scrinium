package clog

import (
	"log/slog"
	"net/http"
	"time"

	"scrinium.dev/internal/slogx"
)

// statusRecorder captures the response status so the request log can report
// it. net/http's ResponseWriter offers no read-back, and the WebDAV handler's
// own Logger callback surfaces only errors, not the status it sent.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

// Middleware logs one Debug line per HTTP request — method, status, duration,
// path — tagged with surface ("webdav" / "webview"). It is the per-request
// trace that --debug turns on; PUT shows uploads, DELETE shows deletes, and so
// on. When Debug is off it is a straight pass-through: the ResponseWriter is
// not wrapped, so streaming optimisations (Flush, ReadFrom) are preserved on
// the hot path. Genuine failures are logged by each surface itself at Error
// level, independent of this middleware.
func Middleware(log *slog.Logger, surface string) func(http.Handler) http.Handler {
	log = slogx.OrDiscard(log)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !log.Enabled(r.Context(), slog.LevelDebug) {
				next.ServeHTTP(w, r)
				return
			}
			rec := &statusRecorder{ResponseWriter: w}
			start := time.Now()
			next.ServeHTTP(rec, r)
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			log.LogAttrs(r.Context(), slog.LevelDebug, surface,
				slog.String("method", r.Method),
				slog.Int("status", status),
				slog.Duration("dur", time.Since(start).Round(100*time.Microsecond)),
				slog.String("path", r.URL.Path),
			)
		})
	}
}
