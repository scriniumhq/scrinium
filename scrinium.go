package scrinium

import (
	"sync"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/projection"
	"scrinium.dev/engine/projection/fsindex"
	"scrinium.dev/engine/store"
)

// Scrinium holds the long-lived resources every Scrinium-backed
// application shares: an open Store, a StoreIndex, a projection
// View, an FSOps facade, plus a boot-unique MountSession used in
// stats and as a tiebreaker. Construct with Open or Init; shut
// down with Close.
//
// Hosts consume the fields directly. Surfaces (FUSE, WebDAV,
// HTTP, gRPC, etc.) read from View and FSOps; admin tooling
// reaches into Store and Index for management operations.
//
// The struct is intentionally a plain bag of pointers — no
// internal state, no methods beyond the lifecycle ones. That
// leaves hosts free to wrap or extend it without inheritance
// gymnastics.
type Scrinium struct {
	// Config is the validated config Scrinium was opened with.
	// Surfaces consult routing/policy fields here when they
	// need them past Open.
	Config Config

	// Store is the high-level CAS store. Surfaces use it for
	// Put/Get and for capacity queries (stats endpoints).
	Store store.Store

	// Index is the metadata index. Surfaces rarely touch this
	// directly; it's exposed for diagnostics like the
	// extension list rendered in stats.
	Index index.StoreIndex

	// View is the read-side projection of the store: trees by
	// path, by date, etc. Both FUSE and WebDAV adapters route
	// reads through it.
	View *projection.View

	// FSOps is the read/write filesystem facade — the layer
	// FUSE and WebDAV adapters wrap. Carries the mount session
	// and editing policy resolved from Config.
	FSOps *projection.FSOps

	// FSIndex is the filesystem-projection index extension
	// kept in scope so it can be referenced after Open
	// (e.g. ListExtensions for stats).
	FSIndex *fsindex.Extension

	// MountSession is the boot-unique identifier this Scrinium
	// instance presents in stats and uses as a tiebreaker.
	// Generated at Open or Init time.
	MountSession domain.SessionID

	// closeOnce makes Close idempotent. The first call shuts
	// resources down and stores the joined error in closeErr;
	// subsequent calls return that same error without doing
	// anything. This matches the io.Closer convention used by
	// *os.File, *sql.DB, and most stdlib closers.
	closeOnce sync.Once
	closeErr  error
}

// ListExtensions returns the registered index extensions.
// Empty slice on backends that don't support extension listing
// — the surface presents "no extensions" rather than failing.
//
// The lookup is cheap (in-memory map on the backend) so we
// don't cache; surfaces call this on every stats render.
func (s *Scrinium) ListExtensions() []index.ExtensionInfo {
	lister, ok := s.Index.(index.ExtensionLister)
	if !ok {
		return nil
	}
	return lister.ListExtensions()
}
