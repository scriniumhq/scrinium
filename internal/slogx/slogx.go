package slogx

import "log/slog"

// Discard is the shared no-op logger used wherever a component was built
// without one. slog.DiscardHandler (Go 1.24+) reports Enabled == false, so
// every call short-circuits before any argument is formatted; one shared
// instance is therefore enough for the whole process and allocates nothing
// per call site.
var Discard = slog.New(slog.DiscardHandler)

// OrDiscard returns l, or the shared Discard logger when l is nil. It is
// the canonical "logger() never returns nil" helper: a field that may be
// nil is wrapped once at the read boundary so callers log unconditionally
// and rely on Enabled-gating for cost on hot paths.
func OrDiscard(l *slog.Logger) *slog.Logger {
	if l == nil {
		return Discard
	}
	return l
}
