package sqlite

import (
	"strings"
	"time"

	"scrinium.dev/errs"
	"scrinium.dev/event"
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

// Is reports whether target is the ErrLeaseHeld sentinel. Direct
// comparison rather than errors.Is recursion: by contract target
// is the leaf sentinel the caller is matching against, and the
// outer errors.Is call already walked the tree before invoking us.
func (e *busyError) Is(target error) bool {
	return target == errs.ErrLeaseHeld
}

// emitContention publishes index.contention_error if a contention
// condition was just observed. waitedFor is the wall-clock duration
// the caller spent waiting (typically from a timer started before
// the operation).
func (i *Index) emitContention(operation string, waitedFor time.Duration) {
	i.publish(event.EventIndexContentionError, event.IndexContentionErrorPayload{
		Operation: operation,
		WaitedFor: waitedFor,
	})
}

// emitLatency publishes index.write_latency for a single mutating
// operation. We always emit on the success path; on failure we
// still emit so dashboards can compare success/failure latency
// distributions.
func (i *Index) emitLatency(operation string, dur time.Duration) {
	i.publish(event.EventIndexWriteLatency, event.IndexWriteLatencyPayload{
		Operation: operation,
		Duration:  dur,
	})
}

// observe wraps a mutating Index operation with the standard
// instrumentation contract: latency emission on every exit
// (success or failure), contention emission on busy/locked
// errors, and uniform classifyError translation.
//
// Every public mutating method on Index that emits metrics
// goes through this helper. Calling it makes it impossible
// to forget the contention emission on a new method, and
// keeps the metric label (operation name) co-located with
// the work it measures.
func (i *Index) observe(op string, fn func() error) error {
	start := time.Now()
	defer func() { i.emitLatency(op, time.Since(start)) }()

	err := fn()
	if isBusyError(err) {
		i.emitContention(op, time.Since(start))
	}
	return classifyError(err)
}
