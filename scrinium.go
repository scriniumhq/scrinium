package scrinium

import (
	"github.com/rkurbatov/scrinium/engine/core"
	"github.com/rkurbatov/scrinium/engine/index"
	"github.com/rkurbatov/scrinium/engine/index/sqlite"
	"github.com/rkurbatov/scrinium/engine/projection"
	"github.com/rkurbatov/scrinium/engine/projection/fsindex"
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
	Store core.Store

	// Index is the metadata index. Surfaces rarely touch this
	// directly; it's exposed for diagnostics like the
	// extension list rendered in stats.
	Index core.StoreIndex

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
	MountSession string
}

// indexWithExtensions is the optional capability some
// StoreIndex backends expose for registering index extensions
// (fsindex, future audit/full-text/etc.). sqlite implements
// it; future backends that also support extensions just need
// to expose the same Extensions() method.
//
// Lives here rather than in core to avoid the core ↔ index
// import cycle that would result from declaring it as part of
// core.StoreIndex. We type-assert against this interface in
// Open and skip extension setup gracefully on backends that
// don't support it.
type indexWithExtensions interface {
	Extensions() index.ExtensionRegistry
}

// indexWithExtensionList is the read-side capability for
// enumerating registered extensions. sqlite returns its own
// concrete ExtensionInfo type; we couple to it here because
// pretending the type is generic would require either a
// shared index.ExtensionInfo (not currently defined) or a
// type-asserting interface that re-exposes the same shape
// (more boilerplate than the import).
//
// When postgres adds an equivalent ListExtensions returning
// its own type, we'll lift ExtensionInfo into the shared
// index package and switch this to that type — small change
// once the second concrete type exists.
type indexWithExtensionList interface {
	ListExtensions() []sqlite.ExtensionInfo
}

// ExtensionInfo is the public, scrinium-level view of a
// registered index extension. Mirrors what backends expose;
// surfaces (stats endpoints, debug pages) consume this rather
// than reaching into the backend type.
type ExtensionInfo struct {
	Name          string
	SchemaVersion int
}

// ListExtensions returns the registered index extensions.
// Empty slice on backends that don't support extensions —
// the surface presents "no extensions" rather than failing.
//
// The lookup is cheap (in-memory map on the backend) so we
// don't cache; surfaces call this on every stats render.
func (s *Scrinium) ListExtensions() []ExtensionInfo {
	lister, ok := s.Index.(indexWithExtensionList)
	if !ok {
		return nil
	}
	src := lister.ListExtensions()
	out := make([]ExtensionInfo, 0, len(src))
	for _, e := range src {
		out = append(out, ExtensionInfo{
			Name:          e.Name,
			SchemaVersion: e.SchemaVersion,
		})
	}
	return out
}
