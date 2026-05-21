package index

import (
	"context"
	"errors"

	"scrinium.dev/engine/domain"
)

// IndexExtension is the contract host-side index extensions
// satisfy. An extension lives inside a StoreIndex backend,
// shares its transactions, and exposes its own read API to the
// host.
//
// Two-paragraph mental model:
//
// (1) Subscriptions. Extensions declare which mutations they
// care about via Subscribe. The backend dispatches matching
// events into Apply WITHIN the same transaction as the main
// index write — so an extension cannot drift from the main
// index state. A failure in Apply rolls the whole transaction
// back, including the main write.
//
// (2) Storage. Extensions own no SQL, no DB handles, no
// migration code: they put bytes into a backend-agnostic
// ExtensionStore keyed by (table, key). The backend translates
// to its own substrate. Tables are namespace-prefixed by
// extension Name to prevent collisions between extensions.
//
// Contract spec: 3. Contracts/06 Index Extensions.md.
// Behaviour and sqlite implementation: 4. API Reference/16.
type IndexExtension interface {
	// Name is the stable identifier for this extension. Used
	// as the namespace prefix in ExtensionStore. Must be
	// unique among extensions registered to the same backend.
	// Recommended: dotted, lower-case, like "scrinium.fsindex".
	Name() string

	// SchemaVersion is the current data layout version. The
	// backend persists the version of the most-recent successful
	// Setup; on a later registration with a different version,
	// Setup is called with the stored value as oldVersion and
	// the extension migrates.
	SchemaVersion() int

	// Subscribe returns the event kinds this extension wants to
	// observe. An empty slice means "no subscriptions" — useful
	// for read-only extensions that initialise data in Setup
	// and never react to mutations. Called once at Register;
	// the result is cached.
	Subscribe() []EventKind

	// Setup runs once per registration, inside the registration
	// transaction. oldVersion is 0 on the first registration; a
	// positive value is the persisted SchemaVersion from a
	// previous run. Implementations migrate from oldVersion to
	// SchemaVersion().
	Setup(ctx context.Context, store ExtensionStore, oldVersion int) error

	// Apply is invoked for each subscribed event, inside the
	// backend's transaction. Mutations through store land in
	// the same transaction as the main index write; an error
	// returned here aborts both.
	Apply(ctx context.Context, store ExtensionStore, kind EventKind, args EventArgs) error

	// Close releases extension-side resources. Backend storage
	// remains owned by the StoreIndex.
	Close() error
}

// EventKind names an index-level operation an extension can
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

	// EventKindBlobRebound corresponds to StoreIndex.RebindBlob.
	// EventArgs.BlobRefs[0] is the rebound blob ref; the rest of
	// EventArgs is zero.
	EventKindBlobRebound
)

// String returns a human-readable name for the kind. Used in
// error messages and diagnostic events.
func (k EventKind) String() string {
	switch k {
	case EventKindManifestIndexed:
		return "ManifestIndexed"
	case EventKindManifestDeleted:
		return "ManifestDeleted"
	case EventKindBlobRebound:
		return "BlobRebound"
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

// ExtensionStore is the backend-agnostic data plane an extension
// uses for its own state. All mutations are scoped to the
// surrounding transaction; reads see committed state.
//
// Tables are auto-namespaced by extension Name; the same `table`
// argument from two extensions does not collide.
type ExtensionStore interface {
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

// ExtensionRegistry is the surface returned by
// StoreIndex.Extensions. The only mutation is Register; backends
// expose nothing else through this interface.
type ExtensionRegistry interface {
	// Register attaches an extension to the index. Setup runs
	// in a single transaction; failure rolls back any work the
	// extension did during Setup, persisted schema_version is
	// not bumped, and no subscriptions are recorded.
	//
	// Returns ErrExtensionExists if Name() collides with an
	// already-registered extension.
	// Returns ErrSchemaRegression if SchemaVersion() is less
	// than the persisted value for this Name().
	Register(ctx context.Context, ext IndexExtension) error
}

// Sentinel errors. Wrapped by backends with %w so callers can
// errors.Is against them.
var (
	// ErrStopScan is returned by an ExtensionStore.Scan callback
	// to stop the iteration without surfacing it as an error.
	ErrStopScan = errors.New("scrinium/index: stop scan")

	// ErrExtensionExists is returned by Register when an
	// extension with the same Name() is already registered.
	ErrExtensionExists = errors.New("scrinium/index: extension already registered")

	// ErrSchemaRegression is returned by Register when the
	// extension's SchemaVersion() is less than the persisted
	// value. Backends do not auto-downgrade.
	ErrSchemaRegression = errors.New("scrinium/index: extension schema version regressed")

	// ErrBackendMismatch is returned by extension Setup when
	// the backend does not implement an interface the extension
	// requires (typically a backend escape hatch like SQLBackend).
	ErrBackendMismatch = errors.New("scrinium/index: extension incompatible with backend")

	// ErrEmptyPrefix is returned by DeletePrefix when called
	// with an empty prefix. "Delete all rows of a table" is an
	// explicit operation; callers must Scan and Delete to get it.
	ErrEmptyPrefix = errors.New("scrinium/index: empty prefix in DeletePrefix")
)

// ExtensionInfo is the public, backend-agnostic descriptor of a
// registered index extension. Surfaces (stats endpoints, debug
// pages, examples) consume this rather than reaching into a
// concrete backend type.
//
// Backends that report registered extensions return slices of
// this type via ExtensionLister. The type is intentionally
// flat — no behaviour, no pointers — so it can travel through
// any layer without dragging dependencies.
type ExtensionInfo struct {
	// Name is the extension's stable identifier
	// (IndexExtension.Name()).
	Name string

	// SchemaVersion is the persisted schema version for this
	// extension on this backend, after the most recent successful
	// Setup. Used to surface migration state.
	SchemaVersion int
}

// ExtensionHost is the optional capability a StoreIndex backend
// exposes when it supports registering host-side extensions.
//
// Backends that support extensions implement this; the rest are
// transparently skipped by callers that type-assert it. Lives
// here (not on store.StoreIndex) so the core package needs no
// import of engine/index — the assertion happens at the wiring
// layer instead.
type ExtensionHost interface {
	// Extensions returns the registry through which IndexExtension
	// implementations are attached to this backend.
	Extensions() ExtensionRegistry
}

// ExtensionLister is the optional capability a StoreIndex backend
// exposes when it can enumerate currently-registered extensions.
//
// Distinct from ExtensionHost (the registration-side capability)
// because read and write surfaces are conceptually independent —
// a future read-only proxy backend might list without registering,
// or a constrained backend might register without listing. In
// practice today's sqlite backend implements both.
//
// Returns an empty slice (never nil) when no extensions are
// registered. Order is unspecified — callers that need stable
// listings sort by Name.
type ExtensionLister interface {
	ListExtensions() []ExtensionInfo
}
