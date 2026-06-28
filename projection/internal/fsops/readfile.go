package fsops

import (
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// readOnlyFile wraps a store.ReadHandle in the File interface,
// returning ErrEditingDisabled for every write/sync method. The
// underlying handle's random-access support is propagated: ReadAt
// works iff the handle supports it.
type readOnlyFile struct {
	rh domain.ReadHandle
}

func (f *readOnlyFile) ReadAt(p []byte, off int64) (int, error) {
	if !f.rh.SupportsRandomAccess() {
		// Ops Handle contract requires ReadAt; fall back to the
		// stream-only error so callers can detect the situation
		// and degrade if they have an alternative path.
		return 0, fmt.Errorf("%w: read handle has no random access",
			errs.ErrArtifactUnreadable)
	}
	return f.rh.ReadAt(p, off)
}

func (f *readOnlyFile) WriteAt(p []byte, off int64) (int, error) {
	return 0, fmt.Errorf("%w: WriteAt on read-only handle",
		errs.ErrEditingDisabled)
}

func (f *readOnlyFile) Sync() error {
	// Sync on a read-only handle is a no-op on POSIX, but we
	// surface it as disabled to keep the semantics predictable —
	// any caller invoking Sync intends a write barrier.
	return fmt.Errorf("%w: Sync on read-only handle",
		errs.ErrEditingDisabled)
}

func (f *readOnlyFile) Truncate(size int64) error {
	return fmt.Errorf("%w: Truncate on read-only handle",
		errs.ErrEditingDisabled)
}

func (f *readOnlyFile) Close() error { return f.rh.Close() }
