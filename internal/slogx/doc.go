// Package slogx holds the engine's shared *slog.Logger helpers.
//
// The whole engine follows ADR-60: diagnostic logging is optional and
// silent by default. A component handed no logger must still be safe to
// call, so every such site substitutes a no-op logger rather than
// guarding each call. Before this package, that "nil → discard" guard and
// a private discardLogger var were copied into every component that could
// be built without a logger (store core, systemStore, agent baseState).
// This package is the single home for that one decision.
package slogx
