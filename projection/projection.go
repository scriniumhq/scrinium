// Package projection builds virtual filesystem-like views over the
// flat content-addressed store. The package is the seam at which
// transport-specific daemons (cmd/scrinium-fuse, cmd/scrinium-webdav)
// plug in: projection itself does no syscalls and no networking.
//
// Architecture: View is the in-memory tree (read side) populated by
// backfill from a ProjectionSource. FSOps adds the write side —
// create/unlink/rename/setattr — and is the place where scratch
// buffering, path-level locks and editing policies live. Together
// they cover ~80% of what FUSE and WebDAV daemons need; the
// transport layer is a thin dispatcher.
//
// Schemas describing how artifacts map to filesystem paths live in
// subpackages (projection/fsmeta is the standard one). They are
// pluggable through the PathResolver function passed to NewView.
//
// Specification: docs/3 §5 Projection API, docs/4 §13 Projection,
// docs/4 §14 FUSE Mount.
package projection

import (
	"context"
	"encoding/json"
	"iter"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/event"
)

// --- Source ---

// ProjectionSource is the minimal contract for an artifact source
// supplying a View. Satisfied by store.DataStore and the multistore
// without additional code. Extended abilities (StorageFacet
// population) are detected on the View side via a type assertion
// when needed — keeps the multistore out of projection's import graph.
type ProjectionSource interface {
	Walk(ctx context.Context, namespace string, cb func(domain.Manifest) error) error
	Get(ctx context.Context, id domain.ArtifactID, opts ...store.GetOption) (domain.ReadHandle, error)
}

// SourceKind labels the type of source backing a View. Governs
// whether StorageFacet is meaningful for a Node.
type SourceKind string

const (
	// SourceKindStore — a single store.DataStore. StorageFacet is
	// always nil.
	SourceKindStore SourceKind = "store"

	// SourceKindMultistore — a multistore with MultistoreIndex.
	// StorageFacet is populated.
	SourceKindMultistore SourceKind = "multistore"
)

// --- PathResolver ---

// PathResolver extracts a virtual path from a manifest. Implementing
// it is how a host plugs a metadata schema into the projection.
//
// Returns:
//   - (path, true) when the artifact carries a recognised schema
//     and a valid path.
//   - ("", false) when the artifact is opaque to this resolver.
//
// Pure: the same Manifest must always produce the same result —
// the View caches the decision and any non-determinism shows up as
// stale-tree bugs.
//
// Standard implementation for the filesystem schema is
// projection/fsmeta.Resolver.
type PathResolver func(m domain.Manifest) (path string, ok bool)

// --- Node and facets ---

// FilesystemFacet is the minimal POSIX-shaped view of a Node:
// what every consumer of the View needs regardless of schema.
// Always populated, including for virtual directories
// synthesised from grouping.
//
// POSIX attributes (mode, uid, gid) are NOT in this facet:
// they belong to the filesystem schema (fsmeta.FileSystem) and
// are materialised by FSOps when the consumer crosses the
// transport boundary (FUSE/WebDAV). Storing them here would
// commit View to a single schema and pre-empt the consumer's
// policy. Email- or other non-POSIX projections reuse Node
// without paying for unused POSIX fields.
type FilesystemFacet struct {
	Name    string
	Path    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// ArtifactFacet carries the CAS metadata of a concrete artifact.
// Populated for file nodes; nil for virtual directories.
type ArtifactFacet struct {
	ArtifactID  domain.ArtifactID
	ContentHash domain.ContentHash
	BlobRef     domain.BlobRef
	Namespace   string
	SessionID   domain.SessionID
	CreatedAt   time.Time
	Type        domain.ManifestType

	// Ext carries the engine-extension metadata block (fsmeta
	// and friends — keys the engine reads through its own
	// decoders). Per ADR-54 the Usr block is intentionally not
	// surfaced at facet level; host applications consume Usr
	// through a different read path.
	Ext json.RawMessage
}

// StorageFacet carries placement data within a multistore stack.
// Populated only when SourceKind == SourceKindMultistore.
type StorageFacet struct {
	StoreID  domain.StoreID
	RefCount int
}

// Node is one entry in the View. FS is always populated; Artifact
// for files; Storage only on a multistore source.
type Node struct {
	FS       FilesystemFacet
	Artifact *ArtifactFacet
	Storage  *StorageFacet
}

// NodeSeq is a sequence of nodes with an optional error per
// position (the standard iter.Seq2 pattern for fallible streams).
type NodeSeq = iter.Seq2[Node, error]

// --- View configuration ---

// RootView selects which logical tree appears at the root of the
// View. The chosen tree does not duplicate into the service
// directory of a FUSE mount.
type RootView string

const (
	RootByPath      RootView = "by-path" // default
	RootBySession   RootView = "by-session"
	RootByNamespace RootView = "by-namespace"
	RootByDate      RootView = "by-date"
	RootByArtifact  RootView = "by-artifact"
	RootByOrphaned  RootView = "by-orphaned"
)

// PathFallback governs how artifacts without a resolver path are
// surfaced.
type PathFallback string

const (
	// FallbackOrphaned (default) — no by-path entry. Artifacts
	// remain reachable through by-artifact and the orphaned/
	// service tree.
	FallbackOrphaned PathFallback = "orphaned"

	// FallbackSynthetic — artifacts get a synthetic path derived
	// from namespace + session + short ArtifactID. Mixes real and
	// synthetic paths in by-path; for debugging on noisy stores.
	FallbackSynthetic PathFallback = "synthetic"
)

// ViewFilter restricts which manifests are admitted into the View
// during backfill. All non-zero conditions combine by AND.
type ViewFilter struct {
	Namespace string
	SessionID domain.SessionID
	Prefix    string
}

// ViewOption is the option type passed to NewView.
type ViewOption func(*viewOptions)

type viewOptions struct {
	resolver PathResolver
	rootView RootView
	fallback PathFallback
	filter   ViewFilter
	bus      event.EventBus

	// extSource is an optional bulk source of manifest
	// metadata used by backfill to skip per-manifest
	// Source.Get round-trips. Set via WithExtSource or
	// WithFSIndex (the latter is a typed convenience for the
	// common projection/fsindex case).
	extSource ExtSource
}

// ExtSource is the contract View backfill uses to fetch the
// engine-extension block (Manifest.Ext) in bulk. Source.Walk
// normally returns stripped manifests (the index is the
// routing layer, not the content store) — without an
// ExtSource, View has to round-trip Source.Get for every
// manifest just to recover Ext, which is N+1.
//
// An ExtSource — typically an index extension that persisted
// the ext block at write time — answers Ext(id) in O(log N)
// from local storage. View.backfill skips the per-manifest Get
// when one is configured.
//
// The interface is schema-agnostic: the source returns the raw
// json.RawMessage that Manifest.Ext held at write time. View
// consumers (FSOps and friends) decode into whatever schema
// they care about (fsmeta, email, archive).
type ExtSource interface {
	// Ext returns the ext-block bytes for the given artifact
	// id. (raw, true, nil) — found; (nil, false, nil) — not
	// present; the third return is reserved for
	// infrastructure errors (DB I/O failure).
	Ext(id domain.ArtifactID) (json.RawMessage, bool, error)
}

// WithExtSource installs a bulk metadata source for
// backfill. When set, View.backfill consults the source instead
// of round-tripping Source.Get for each manifest. A miss
// (artifact not indexed by the source) falls back to Source.Get
// transparently — the option is a performance hint, not a
// correctness requirement.
func WithExtSource(ms ExtSource) ViewOption {
	return func(o *viewOptions) { o.extSource = ms }
}

// WithFSIndex is a typed convenience for the projection/fsindex
// case: pass the registered *fsindex.Extension and it doubles as
// a ExtSource. Equivalent to WithExtSource(fsidx).
//
// Implemented at the package level via an interface to avoid
// taking a hard dependency on projection/fsindex from
// projection — fsindex imports projection's fsmeta, so a back-
// edge would cycle.
func WithFSIndex(fsidx ExtSource) ViewOption {
	return WithExtSource(fsidx)
}

// WithPathResolver registers the path-extraction function. Without
// it the by-path tree contains only artifacts produced by the
// fallback (when FallbackSynthetic) or is empty.
func WithPathResolver(r PathResolver) ViewOption {
	return func(o *viewOptions) { o.resolver = r }
}

// WithRootView selects the tree that occupies the View root. The
// default is RootByPath. The choice is informational for the View
// itself; transports (FUSE) react to it by hiding the same tree
// from the service directory.
func WithRootView(rv RootView) ViewOption {
	return func(o *viewOptions) { o.rootView = rv }
}

// WithFallback governs how artifacts without a resolver path are
// represented. Default: FallbackOrphaned.
func WithFallback(fb PathFallback) ViewOption {
	return func(o *viewOptions) { o.fallback = fb }
}

// WithFilter restricts the View to a subset of the source. Use for
// namespace-scoped or session-scoped views.
func WithFilter(f ViewFilter) ViewOption {
	return func(o *viewOptions) { o.filter = f }
}

// WithEventBus wires an event bus that receives EventViewRebuilt
// after backfill and EventPathCollision on each by-path
// collision. nil bus (the default when this option is not used)
// silently drops events.
func WithEventBus(bus event.EventBus) ViewOption {
	return func(o *viewOptions) { o.bus = bus }
}

// ViewStats holds the aggregate counters of a View. Populated
// during backfill and updated by Add/Remove/Move calls.
type ViewStats struct {
	TotalNodes     int64
	TotalBytes     int64
	SessionCount   int64
	NamespaceCount int64
	OrphanedCount  int64
	CollisionCount int64
	ByStore        map[string]int64
	TransitCount   int64
}

// --- Events ---

const (
	// EventPathCollision is emitted when two artifacts compete for
	// the same by-path entry; the loser stays accessible through
	// by-artifact.
	EventPathCollision = "projection.path_collision"

	// EventViewRebuilt is emitted after a successful backfill.
	EventViewRebuilt = "projection.view_rebuilt"
)

// PathCollisionPayload carries the resolution data of a path
// collision. Winner is the artifact now holding the path; Loser
// is the artifact that lost it (still reachable through
// by-artifact).
type PathCollisionPayload struct {
	Path   string
	Winner domain.ArtifactID
	Loser  domain.ArtifactID
}

// ViewRebuiltPayload carries timing and counts of a backfill
// completion. NodeCount is the total number of nodes across every
// tree (one file artifact may appear under several trees).
type ViewRebuiltPayload struct {
	Duration  time.Duration
	NodeCount int64
}

// Projection bundles the read-side View with the optional read/write
// FSOps facade — the two ends of one projection over a store. They are
// always used together (a daemon reads trees through View and mutates
// through FSOps), and FSOps is constructed from a View, so pairing them
// in one value matches how callers consume them.
//
// FSOps is nil for a read-only projection; View is always present.
type Projection struct {
	// View is the read-side: the materialised trees (by-path, by-date,
	// …) over the store. Always non-nil in a built Projection.
	View *View

	// FSOps is the read/write filesystem facade over View. Nil when the
	// projection is read-only.
	FSOps *FSOps
}

// Close releases the projection. FSOps holds no resources beyond the
// View it wraps, so closing the View is sufficient; the method exists
// so a Projection composes as an io.Closer alongside the store.
func (p *Projection) Close() error {
	if p == nil || p.View == nil {
		return nil
	}
	return p.View.Close()
}
