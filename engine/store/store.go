package store

import (
	"context"
	"io"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
)

// Store is the union of DataStore and AdminStore. Returned by
// InitStore and OpenStore. By passing only DataStore (and not Store)
// to a decorator, the host application guarantees at the
// type level that the administrative methods are unreachable from
// that code.
type Store interface {
	DataStore
	AdminStore
}

// DataStore is the artifact-facing API. This interface is sufficient
// for client code, decorators (bundler.Wrapper, chunker.Wrapper),
// It does not expose administrative operations.
type DataStore interface {
	// I/O.

	// Put stores an artifact: it runs Payload through the Pipeline,
	// performs deduplication, and writes the blob and the manifest.
	// It returns ArtifactID — the cryptographic hash of the
	// serialised manifest file.
	Put(ctx context.Context, a domain.Artifact, opts ...domain.PutOption) (domain.ArtifactID, error)

	// PutBlob writes an anonymous blob without creating a manifest.
	// Not a client method: it is used by level-3 decorators
	// (chunker.Wrapper for writing anonymous chunks).
	PutBlob(ctx context.Context, r io.Reader, blobType domain.BlobType) (domain.ContentHash, error)

	// Get opens an artifact for reading. It returns a ReadHandle —
	// a streaming primitive with lazy resolution of the physical
	// address.
	Get(ctx context.Context, id domain.ArtifactID, opts ...domain.GetOption) (domain.ReadHandle, error)

	// Management and verification.

	// Delete performs a logical deletion: it removes the manifest
	// file from disk and decrements ref_count for every related blob
	// in a single StoreIndex transaction. Physical removal is
	// delegated to the GC Agent.
	Delete(ctx context.Context, id domain.ArtifactID) error

	// Verify performs a full integrity check of an artifact: it
	// re-hashes the manifest and the blob and runs the inverse
	// Pipeline with ContentHash verification. It ignores
	// CapNativeChecksum and VerifyOnRead.
	Verify(ctx context.Context, id domain.ArtifactID) error

	// VerifyBlobRef is the same plaintext integrity check as Verify
	// but keyed by a physical blob_ref. It is the Scrub Agent's
	// per-blob step (ListUnverifiedBlobs yields blob_refs); it loads
	// any one consuming manifest to recover the pipeline, re-hashes,
	// and on mismatch publishes EventScrubFailed and returns
	// errs.ErrCorruptedBlob. A blob_ref with no consuming manifest
	// returns errs.ErrArtifactNotFound (skip, not corruption).
	VerifyBlobRef(ctx context.Context, blobRef string) error

	// VerifyManifest checks a manifest's integrity only (its file
	// still hashes to its ArtifactID), without touching the blob —
	// the cheap half of verification. It is the Scrub Agent's cascade
	// step over a verified blob's consumers, and the whole check for
	// Inline artifacts. On corruption it publishes EventScrubFailed
	// and returns the error; a missing manifest returns
	// errs.ErrArtifactNotFound.
	VerifyManifest(ctx context.Context, id domain.ArtifactID) error

	// RollbackSession is a group rollback of every artifact carrying
	// the given SessionID. It is idempotent: when interrupted, a
	// repeat call resumes the cleanup.
	RollbackSession(ctx context.Context, sessionID domain.SessionID) error

	// Iteration and introspection.

	// Walk iterates over user manifests. namespace = "*" — every user
	// namespace; an empty string — only the default one. system.*
	// artifacts are unreachable here; they live behind System().
	Walk(ctx context.Context, namespace string, cb func(domain.Manifest) error) error

	// WalkByExt iterates over user manifests whose projected ext field
	// extName.field equals value (proj_ext, §9.6). It is the extension-
	// agnostic, equality-only listing primitive: the core attaches NO meaning
	// to extName/field — a namespace extension lists its artifacts via
	// WalkByExt("namespace", "nsid", <nsid>); other projecting extensions use
	// their own fields. The yielded Manifest carries index-resident fields
	// only (no manifest-file I/O); Ext is not hydrated — read it via Get if
	// needed. A field/operator-discovery selector that also folds in native
	// fields and prefix/range is future work (Walkable-Fields rationale).
	WalkByExt(ctx context.Context, extName, field, value string, cb func(domain.Manifest) error) error

	// Capacity returns aggregated storage metrics.
	Capacity(ctx context.Context) (domain.StorageInfo, error)
}

// AdminStore is the administrative API. It is required by the Store
// owner: the code that called InitStore / OpenStore. It is not
// passed to decorators.
type AdminStore interface {
	// State returns the current state of the Store.
	State() domain.StoreState

	// Capabilities returns the bitmask of the underlying driver's
	// abilities. It is stable for the entire lifetime of the Store.
	Capabilities() driver.CapabilityMask

	// Unlock transitions the Store from StateLocked to StateUnlocked.
	// Idempotent: calling it again in StateUnlocked is a no-op.
	Unlock(ctx context.Context) error

	// ExportRecoveryKit returns the current Recovery Kit as bytes.
	// Available only in StateUnlocked and StateDegraded.
	ExportRecoveryKit(ctx context.Context) ([]byte, error)

	// RotateKEK re-encrypts the DEK with a new KEK. The data on disk
	// is not rewritten. The PassphraseProvider configured on the
	// Store is called twice — once for the current passphrase
	// (Reason="unlock", same as Store.Unlock) and once for the
	// replacement (Reason="kek_rotation").
	//
	// After RotateKEK the previous Recovery Kit is invalid; the
	// host is required to obtain a new one through ExportRecoveryKit
	// and persist it before reporting success to the user.
	RotateKEK(ctx context.Context) error

	// SetPassphrase enables encryption on a Store that was
	// initialised with a plaintext DEK. It calls the configured
	// PassphraseProvider once with Reason="set_passphrase" to obtain
	// the new passphrase, derives a KEK, wraps the existing DEK,
	// and persists the descriptor. The data on disk is not
	// rewritten.
	//
	// Refuses with errs.ErrPassphraseAlreadySet when the DEK is
	// already wrapped — use RotateKEK in that case. After
	// SetPassphrase the host MUST persist the freshly-issued
	// Recovery Kit through ExportRecoveryKit.
	SetPassphrase(ctx context.Context) error

	// SetMaintenanceMode transitions the Store into a maintenance
	// mode. Used before running a Maintenance Agent.
	SetMaintenanceMode(ctx context.Context, mode domain.MaintenanceMode) error

	// UpdateConfig updates the mutable parameters of StoreConfig.
	// Immutable parameters cannot be changed — errs.ErrConfigMismatch.
	// Not yet wired: returns errs.ErrNotImplemented.
	UpdateConfig(ctx context.Context, cfg domain.StoreConfig) error

	// Config returns a snapshot of the active StoreConfig — the
	// projection persisted as the active system/config version, with
	// defaults applied. Read-only; mutations of the returned value
	// have no effect on the running Store.
	Config() domain.StoreConfig

	// ConfigHistory returns the full history of configuration
	// versions, the active version first.
	ConfigHistory(ctx context.Context) ([]domain.StoreConfig, error)

	// Close releases secrets held by the Store and transitions it
	// to a terminal state. After Close:
	//
	//   - The in-memory DEK is wiped.
	//   - The capability token (if any) is wiped.
	//   - The default StaticKeyResolver (if installed) drops its
	//     internal copy of the DEK.
	//   - The Store's state is set to Locked.
	//   - The Store's StoreIndex is NOT closed: the host owns the
	//     StoreIndex's lifetime (DI contract: see WithStoreIndex)
	//     and must call StoreIndex.Close after this method
	//     returns.
	//
	// Idempotent: a second Close on an already-closed Store
	// returns nil. Operations on a closed Store return an
	// implementation-defined error; do not call Close while reads
	// or writes are in flight.
	//
	// The intended caller is the host application's
	// graceful-shutdown path.
	Close() error

	// RunMaintenance executes a one-shot MaintenanceAgent under the
	// Store: it Validates the agent against the current Store state
	// and, if that passes, runs it to completion, returning the
	// agent's AgentResult. The agent owns its maintenance lease and
	// its own progress/outcome events (it is constructed with the
	// event bus); RunMaintenance is the sanctioned entry point that
	// guarantees Validate-before-Run ordering and lives on AdminStore
	// so DataStore consumers cannot start an agent. The host is
	// expected to have set the maintenance mode the agent requires
	// (see SetMaintenanceMode); Validate reports a mismatch. The
	// contract lives in domain (domain.MaintenanceAgent).
	RunMaintenance(ctx context.Context, agent domain.MaintenanceAgent) (*domain.AgentResult, error)

	// System returns the facade for engine-internal service artifacts
	// (configuration, agent cursors, index snapshots, …). Reached only
	// through AdminStore, so DataStore consumers cannot see system state.
	System() SystemStore
}

// SystemArtifact is an engine-internal service artifact, addressed by a
// slash-separated Name rather than by content hash. Unlike a data-plane
// domain.Artifact it carries no Ext/Usr metadata — system payloads are
// small, opaque service blobs (config versions, agent cursors, index
// snapshots). The Name is the address: Put writes the payload as a new
// version of the name; Get reads the active version; Delete removes the
// name. Versioning, activation (max seq), exclusive-create publishing,
// and verify-on-read integrity live in
// engine/namedstore (ADR-85).
//
// Named addressing is a deliberately small facility for the engine's
// own data — not a general user-facing primitive — which is why it
// lives behind AdminStore.System() and uses its own type rather than
// overloading domain.Artifact.
type SystemArtifact struct {
	// Name is the slash-separated name under which the artifact is
	// stored and later retrieved (e.g. "scrub/cursor").
	Name string

	// Payload is the artifact body. System payloads are small enough to
	// buffer in memory.
	Payload io.Reader

	// Keep selects the storage form (ADR-100/101). It is optional:
	//   nil             → the default, keep=1 (atomic versioned "latest",
	//                     no history). Forgetting Keep is safe — it never
	//                     yields the exclusive-cell (lock) form.
	//   *Keep == 0      → exclusive cell: one fixed slot (<name>), no
	//                     versions, overwrite in place (the keep=0 / lock
	//                     form). Opt-in only — build it with KeepCell().
	//   *Keep ∈ [1,255] → versions: <name>/<seq>, active = max(seq),
	//                     pruned to *Keep retained. Build with KeepVersions(n).
	Keep *uint8
}

// KeepCell marks a SystemArtifact as a keep=0 exclusive cell: a single
// fixed slot, overwritten in place (ADR-100/101). The lock form.
func KeepCell() *uint8 { var k uint8; return &k }

// KeepVersions marks a SystemArtifact as keep=n versioned storage
// (<name>/<seq>, active = max, pruned to n retained). n must be ≥ 1; n=0
// is the cell form — use KeepCell for that.
func KeepVersions(n uint8) *uint8 { return &n }

// SystemStore is the facade for engine-internal service artifacts:
// versioned configuration, agent cursors, index snapshots, and the
// like, each addressed by a slash-separated name. Artifacts are stored
// outside the content-addressed index, in their own address space, and
// are invisible to Store.Walk.
type SystemStore interface {
	// Put writes a SystemArtifact in the form its Keep selects (ADR-101):
	// keep=0 overwrites the exclusive cell in place; keep≥1 publishes a
	// new version (active = max seq) and prunes to Keep retained. Keep is
	// optional — nil defaults to keep=1 (versioned latest, no history).
	Put(ctx context.Context, a SystemArtifact) error

	// Get opens the active version (max seq) or, for a keep=0 name, the
	// cell. Returns errs.ErrArtifactNotFound when the name has never been
	// written.
	Get(ctx context.Context, name string) (domain.ReadHandle, error)

	// Delete removes every version AND any cell of name. Idempotent:
	// deleting an absent name returns nil.
	Delete(ctx context.Context, name string) error

	// Walk iterates over every name with the given prefix in
	// alphabetical order, yielding the active manifest for each — both
	// versioned actives and keep=0 cells (e.g. the lease).
	Walk(ctx context.Context, prefix string, cb func(name string, m domain.Manifest) error) error
}
