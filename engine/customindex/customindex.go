package customindex

import (
	"context"
	"errors"

	"scrinium.dev/domain"
)

// CustomIndex is the contract host-side index custom indexes
// satisfy. A custom index lives inside a StoreIndex backend,
// shares its transactions, and exposes its own read API to the
// host.
//
// Two-paragraph mental model:
//
// (1) Subscriptions. CustomIndexes declare which mutations they
// care about via Subscribe. The backend dispatches matching
// events into Apply WITHIN the same transaction as the main
// index write — so a custom index cannot drift from the main
// index state. A failure in Apply rolls the whole transaction
// back, including the main write.
//
// (2) Storage. CustomIndexes own no SQL, no DB handles, no
// migration code: they put bytes into a backend-agnostic
// Substrate keyed by (table, key). The backend translates
// to its own substrate. Tables are namespace-prefixed by
// custom index Name to prevent collisions between custom indexes.
//
// Contract spec: 3. Reference/09 CustomIndex and Search.md.
type CustomIndex interface {
	// Name is the stable identifier for this custom index. Used
	// as the namespace prefix in Substrate. Must be
	// unique among custom indexes registered to the same backend.
	// Recommended: dotted, lower-case, like "scrinium.fsindex".
	Name() string

	// SchemaVersion is the current data layout version. The
	// backend persists the version of the most-recent successful
	// Setup; on a later registration with a different version,
	// Setup is called with the stored value as oldVersion and
	// the custom index migrates.
	SchemaVersion() int

	// Subscribe returns the event kinds this custom index wants to
	// observe. An empty slice means "no subscriptions" — useful
	// for read-only custom indexes that initialise data in Setup
	// and never react to mutations. Called once at Register;
	// the result is cached.
	Subscribe() []EventKind

	// Setup runs once per registration, inside the registration
	// transaction. oldVersion is 0 on the first registration; a
	// positive value is the persisted SchemaVersion from a
	// previous run. Implementations migrate from oldVersion to
	// SchemaVersion().
	Setup(ctx context.Context, store Substrate, oldVersion int) error

	// Apply is invoked for each subscribed event, inside the
	// backend's transaction. Mutations through store land in
	// the same transaction as the main index write; an error
	// returned here aborts both.
	Apply(ctx context.Context, store Substrate, kind EventKind, args EventArgs) error

	// Close releases custom index-side resources. Backend storage
	// remains owned by the StoreIndex.
	Close() error
}

// EventKind names an index-level operation a custom index can
// observe. The set is closed (defined here, exhaustive switch
// in implementations).
type EventKind uint8

const (
	// EventKindManifestIndexed corresponds to StoreIndex.IndexManifest.
	// EventArgs.Manifest carries the full manifest just written;
	// EventArgs.ArtifactID is a duplicate of Manifest.ArtifactID.
	EventKindManifestIndexed EventKind = iota

	// EventKindManifestDeleted corresponds to StoreIndex.DeleteManifest.
	// EventArgs.ArtifactID is the id of the deleted artifact;
	// EventArgs.BlobRefs lists the blobs the deletion decremented;
	// EventArgs.Manifest is zero.
	EventKindManifestDeleted
)

// String returns a human-readable name for the kind. Used in
// error messages and diagnostic events.
func (k EventKind) String() string {
	switch k {
	case EventKindManifestIndexed:
		return "ManifestIndexed"
	case EventKindManifestDeleted:
		return "ManifestDeleted"
	default:
		return "EventKind(?)"
	}
}

// EventArgs carries operation-specific arguments for Apply. Not
// every field is populated for every kind — see the EventKind
// constants for per-kind documentation.
type EventArgs struct {
	Manifest   domain.Manifest
	ArtifactID domain.ArtifactID
	BlobRefs   []string
}

// --- Capability roster (ADR-88) ---
//
// A CustomIndex declares WHAT it can do by implementing optional
// capability sub-interfaces below; the backend detects them by
// assertion (if r, ok := ext.(Resolver); ok), never by a Class field.
// Only Resolver is defined here — it is what the pack/chunk overlay
// needs (ADR-86/87). The accounting/compaction roster (GCParticipant,
// Compactor) and Indexer land with the GC-contract and projection
// work; they are not required to take the core off pack tables.

// PlacementOverlay is the physical location of an artifact whose
// storage is OWNED by an index custom index rather than the core
// (ADR-88/86) — the overlay counterpart of the core's россыпь
// resolution. A packed artifact lives as two slices of a .pack
// volume (its member manifest and its blob) plus the pipeline
// parameters needed to decode the blob.
//
// The type lives in the contract, not in a concrete owner package,
// so the core resolve path can consult any Resolver without
// importing the owner. Owners map their internal representation onto
// this shape.
type PlacementOverlay struct {
	// PackBlobRef is the blob_ref of the .pack volume the slices live
	// in. The driver range-reads this volume.
	PackBlobRef string

	// ManifestOffset/ManifestSize locate the member-manifest slice
	// within the volume.
	ManifestOffset int64
	ManifestSize   int64

	// BlobOffset/BlobSize locate the member-blob slice within the
	// volume.
	BlobOffset int64
	BlobSize   int64

	// PipelineParams carries the decode parameters for the member
	// blob — opaque to the core, handed back to the pipeline.
	PipelineParams []byte
}

// Resolver is the optional capability a CustomIndex implements
// to OVERLAY physical placement for the artifacts it owns
// (ADR-88/86). The core resolves россыпь itself; for anything it
// does not find, it probes registered Resolvers, each covering its
// own structure. The core never branches on artifact type —
// ownership of the index record decides who answers, so a new
// structural kind needs a new owner, not a core edit.
//
// A Resolver is MANDATORY for reading the structure it owns: with no
// owner registered, the artifact is correctly unresolvable
// (structurally, not by hiding). Capability is detected by interface
// assertion, not by Class.
type Resolver interface {
	CustomIndex

	// ResolvePacked reports the placement of an artifact this
	// custom index owns, by its ArtifactID. The second return is false
	// when the artifact is not owned here — the caller continues to
	// the next resolver or falls back to россыпь. Reads see committed
	// state.
	ResolvePacked(ctx context.Context, artifactID domain.ArtifactID) (PlacementOverlay, bool, error)
}

// Substrate is the backend-agnostic data plane a custom index
// uses for its own state. All mutations are scoped to the
// surrounding transaction; reads see committed state.
//
// Tables are auto-namespaced by custom index Name; the same `table`
// argument from two custom indexes does not collide.
type Substrate interface {
	// Put writes (or overwrites) a value for the given key.
	// Idempotent.
	Put(table, key string, value []byte) error

	// Get retrieves the value for a key.
	// (value, true, nil) — key present.
	// (nil, false, nil) — key absent.
	// Errors are reserved for backend-infrastructure failures.
	Get(table, key string) ([]byte, bool, error)

	// Delete removes a key. No-op if the key is absent (no
	// "not found" error).
	Delete(table, key string) error

	// DeletePrefix removes every key whose lexicographic value
	// starts with prefix. An empty prefix is rejected to make
	// "delete all" an explicit, deliberate operation (callers
	// who really want it must Scan and Delete one by one).
	DeletePrefix(table, prefix string) error

	// Scan iterates entries with the given prefix in
	// lexicographic key order. cb returning ErrStopScan stops
	// the walk without an error; any other error is propagated.
	Scan(table, prefix string, cb func(key string, value []byte) error) error

	// Inc atomically adds delta to the int64 value (encoded as
	// big-endian 8 bytes). Creates the key with delta if absent.
	// Returns the new value.
	Inc(table, key string, delta int64) (int64, error)
}

// Registry is the surface returned by
// StoreIndex.CustomIndexes. The only mutation is Register; backends
// expose nothing else through this interface.
type Registry interface {
	// Register attaches a custom index to the index. Setup runs
	// in a single transaction; failure rolls back any work the
	// custom index did during Setup, persisted schema_version is
	// not bumped, and no subscriptions are recorded.
	//
	// Returns ErrAlreadyRegistered if Name() collides with an
	// already-registered custom index.
	// Returns ErrSchemaRegression if SchemaVersion() is less
	// than the persisted value for this Name().
	Register(ctx context.Context, ext CustomIndex) error
}

// Sentinel errors. Wrapped by backends with %w so callers can
// errors.Is against them.
var (
	// ErrStopScan is returned by an Substrate.Scan callback
	// to stop the iteration without surfacing it as an error.
	ErrStopScan = errors.New("scrinium/index: stop scan")

	// ErrAlreadyRegistered is returned by Register when an
	// custom index with the same Name() is already registered.
	ErrAlreadyRegistered = errors.New("scrinium/index: custom index already registered")

	// ErrSchemaRegression is returned by Register when the
	// custom index's SchemaVersion() is less than the persisted
	// value. Backends do not auto-downgrade.
	ErrSchemaRegression = errors.New("scrinium/index: custom index schema version regressed")

	// ErrBackendMismatch is returned by custom index Setup when
	// the backend does not implement an interface the custom index
	// requires (typically a backend escape hatch like SQLBackend).
	ErrBackendMismatch = errors.New("scrinium/index: custom index incompatible with backend")

	// ErrEmptyPrefix is returned by DeletePrefix when called
	// with an empty prefix. "Delete all rows of a table" is an
	// explicit operation; callers must Scan and Delete to get it.
	ErrEmptyPrefix = errors.New("scrinium/index: empty prefix in DeletePrefix")
)

// Info is the public, backend-agnostic descriptor of a
// registered index custom index. Surfaces (stats endpoints, debug
// pages, examples) consume this rather than reaching into a
// concrete backend type.
//
// Backends that report registered custom indexes return slices of
// this type via Lister. The type is intentionally
// flat — no behaviour, no pointers — so it can travel through
// any layer without dragging dependencies.
type Info struct {
	// Name is the custom index's stable identifier
	// (CustomIndex.Name()).
	Name string

	// SchemaVersion is the persisted schema version for this
	// custom index on this backend, after the most recent successful
	// Setup. Used to surface migration state.
	SchemaVersion int
}

// Host is the optional capability a StoreIndex backend
// exposes when it supports registering host-side custom indexes.
//
// Backends that support custom indexes implement this; the rest are
// transparently skipped by callers that type-assert it. Lives
// here (not on store.StoreIndex) so the core package needs no
// import of engine/index — the assertion happens at the wiring
// layer instead.
type Host interface {
	// CustomIndexes returns the registry through which CustomIndex
	// implementations are attached to this backend.
	CustomIndexes() Registry
}

// Lister is the optional capability a StoreIndex backend
// exposes when it can enumerate currently-registered custom indexes.
//
// Distinct from Host (the registration-side capability)
// because read and write surfaces are conceptually independent —
// a future read-only proxy backend might list without registering,
// or a constrained backend might register without listing. In
// practice today's sqlite backend implements both.
//
// Returns an empty slice (never nil) when no custom indexes are
// registered. Order is unspecified — callers that need stable
// listings sort by Name.
type Lister interface {
	ListCustomIndexes() []Info
}
