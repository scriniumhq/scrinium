package sqlite

import (
	"errors"
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/index"
)

// classifyError maps a low-level SQLite error to a core sentinel
// where appropriate. Used at the boundary of every public method.
//
// Both supported drivers (modernc and mattn) surface contention as
// errors whose Error() contains "SQLITE_BUSY" or "SQLITE_LOCKED" or
// the textual "database is locked" / "database table is locked".
// We match by substring rather than by typed error to keep the two
// drivers interchangeable.
//
// On a contention hit we also return the original error wrapped so
// errors.Unwrap exposes it; this makes log messages helpful without
// leaking driver-specific types.
func classifyError(err error) error {
	if err == nil {
		return nil
	}
	if isBusyError(err) {
		// Contention beyond busy_timeout. Surface as errs.ErrLeaseHeld
		// — the caller's natural reaction is "back off and retry"
		// or "give up", which matches the lease-loss semantics.
		return &busyError{cause: err}
	}
	return err
}

// isBusyError detects SQLite contention errors. Driver-agnostic:
// inspects the error text instead of typed errors.
func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "SQLITE_BUSY"),
		strings.Contains(msg, "SQLITE_LOCKED"),
		strings.Contains(msg, "database is locked"),
		strings.Contains(msg, "database table is locked"):
		return true
	}
	return false
}

// busyError wraps a driver-level busy/locked error so callers see
// errs.ErrLeaseHeld via errors.Is while still being able to inspect
// the original cause via errors.Unwrap.
type busyError struct {
	cause error
}

func (e *busyError) Error() string { return errs.ErrLeaseHeld.Error() + ": " + e.cause.Error() }
func (e *busyError) Unwrap() error { return e.cause }
func (e *busyError) Is(target error) bool {
	return errors.Is(target, errs.ErrLeaseHeld)
}

// emitContention publishes index.contention_error if a contention
// condition was just observed. waitedFor is the wall-clock duration
// the caller spent waiting (typically from a timer started before
// the operation).
func (i *Index) emitContention(operation string, waitedFor time.Duration) {
	i.publish(index.EventIndexContentionError, index.IndexContentionErrorPayload{
		Operation: operation,
		WaitedFor: waitedFor,
	})
}

// emitLatency publishes index.write_latency for a single mutating
// operation. We always emit on the success path; on failure we
// still emit so dashboards can compare success/failure latency
// distributions.
func (i *Index) emitLatency(operation string, dur time.Duration) {
	i.publish(index.EventIndexWriteLatency, index.IndexWriteLatencyPayload{
		Operation: operation,
		Duration:  dur,
	})
}
