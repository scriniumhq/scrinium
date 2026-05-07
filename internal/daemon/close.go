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
func (d *Daemon) Close() error {
	var errs []error

	// View doesn't accept a context for shutdown — it does
	// in-process work only. Close error is informational; the
	// View won't be used after this regardless.
	d.View.Close()

	// Index Close is the actual important resource release —
	// it owns the sqlite handle (or future PG connection
	// pool). Errors here matter.
	if err := d.Index.Close(); err != nil {
		errs = append(errs, fmt.Errorf("index close: %w", err))
	}

	// The Store doesn't have its own Close — it's a thin
	// wrapper over the driver and the index, both of which
	// we've already closed (via the index). The driver itself
	// (localfs) holds nothing closable.

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
