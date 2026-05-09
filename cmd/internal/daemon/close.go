package daemon

import (
	"errors"
	"fmt"
)

// Close releases the daemon's resources in reverse order of
// Open. Errors are accumulated; every resource is given a
// chance to close before we return. The first error is
// surfaced (via errors.Join when more than one); the rest
// would have been logged by the resources themselves had
// they wanted to.
//
// Calling Close more than once is a programming error: it
// would attempt to close already-closed resources. Surfaces
// must own the lifecycle and call Close exactly once.
//
// Order matters: Store.Close goes BEFORE Index.Close because
// Store still references Index for any in-flight operation
// teardown, and Store.Close is the step that wipes secrets
// (DEK, capability token, default StaticKeyResolver) — we
// want those gone before we tear down anything else.
func (d *Daemon) Close() error {
	var errs []error

	// View.Close stops backfill goroutines and marks the View
	// terminated. Errors here are surfaced like the others —
	// failure to release a goroutine matters for tests and
	// long-running supervisors.
	if err := d.View.Close(); err != nil {
		errs = append(errs, fmt.Errorf("view close: %w", err))
	}

	// Store.Close wipes secrets (DEK, capability token,
	// default StaticKeyResolver) and transitions to Locked.
	// Idempotent. Errors are surfaced but do not block
	// subsequent shutdown steps.
	if err := d.Store.Close(); err != nil {
		errs = append(errs, fmt.Errorf("store close: %w", err))
	}

	// Index Close releases the sqlite handle (or future PG
	// connection pool). Errors here matter — leaked DB handles
	// keep the file locked on Windows and waste fds elsewhere.
	if err := d.Index.Close(); err != nil {
		errs = append(errs, fmt.Errorf("index close: %w", err))
	}

	// The Driver (localfs) holds nothing closable.

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
