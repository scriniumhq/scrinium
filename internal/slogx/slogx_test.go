// Tests for the shared no-op logger and the nil-guard. They confirm the
// ADR-60 contract: Discard is non-nil and reports Enabled == false at every
// level (so calls short-circuit before formatting), OrDiscard preserves a
// non-nil logger and substitutes Discard for nil, and logging through either
// never panics.
package slogx_test

import (
	"context"
	"log/slog"
	"testing"

	"scrinium.dev/internal/slogx"
)

func TestDiscard_NotNil(t *testing.T) {
	if slogx.Discard == nil {
		t.Fatal("Discard is nil")
	}
}

func TestDiscard_DisabledAtEveryLevel(t *testing.T) {
	ctx := context.Background()
	for _, lvl := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if slogx.Discard.Enabled(ctx, lvl) {
			t.Errorf("Discard.Enabled(%v) = true, want false", lvl)
		}
	}
}

func TestOrDiscard_NilReturnsDiscard(t *testing.T) {
	got := slogx.OrDiscard(nil)
	if got == nil {
		t.Fatal("OrDiscard(nil) returned nil")
	}
	if got != slogx.Discard {
		t.Error("OrDiscard(nil) did not return the shared Discard logger")
	}
	// And the substitute must itself be disabled.
	if got.Enabled(context.Background(), slog.LevelError) {
		t.Error("OrDiscard(nil) result is not a no-op logger")
	}
}

func TestOrDiscard_NonNilReturnedUnchanged(t *testing.T) {
	l := slog.New(slog.DiscardHandler) // a distinct, non-nil logger
	got := slogx.OrDiscard(l)
	if got != l {
		t.Error("OrDiscard(l) did not return the supplied logger")
	}
	if got == slogx.Discard {
		t.Error("OrDiscard(l) replaced a non-nil logger with Discard")
	}
}

func TestLoggingNeverPanics(t *testing.T) {
	// Both the shared Discard and a nil-substituted logger must be safe to
	// call unconditionally (no nil deref, no panic).
	slogx.Discard.Info("info", "k", "v")
	slogx.Discard.With("scope", "test").Error("error")
	slogx.OrDiscard(nil).Warn("warn", slog.Int("n", 1))
}
