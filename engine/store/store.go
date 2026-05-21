package store

import (
	"context"
	"io"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
)

// DataStore is the artifact-facing API. This interface is sufficient
// for client code, decorators (bundler.Wrapper, chunker.Wrapper),
// and Curator. It does not expose administrative operations.
type DataStore interface {
	// I/O.

	// Put stores an artifact: it runs Payload through the Pipeline,
	// performs deduplication, and writes the blob and the manifest.
	// It returns ArtifactID — the cryptographic hash of the
	// serialised manifest file.
	Put(ctx context.Context, a domain.Artifact, opts domain.PutOptions) (domain.ArtifactID, error)

	// PutBlob writes an anonymous blob without creating a manifest.
	// Not a client method: it is used by level-3 decorators
	// (chunker.Wrapper for writing anonymous chunks).
	PutBlob(ctx context.Context, r io.Reader, blobType domain.BlobType) (domain.ContentHash, error)

	// Get opens an artifact for reading. It returns a ReadHandle —
	// a streaming primitive with lazy resolution of the physical
	// address.
	Get(ctx context.Context, id domain.ArtifactID, opts domain.GetOptions) (ReadHandle, error)

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

	// RollbackSession is a group rollback of every artifact carrying
	// the given SessionID. It is idempotent: when interrupted, a
	// repeat call resumes the cleanup.
	RollbackSession(ctx context.Context, sessionID domain.SessionID) error

	// Iteration and introspection.

	// Walk iterates over user manifests. namespace = "*" — every user
	// namespace; an empty string — only the default one.
	// system.* is unreachable through Walk; system artifacts live
	// behind AdminStore.System() per ADR-57.
	Walk(ctx context.Context, namespace string, cb func(domain.Manifest) error) error

	// Capacity returns aggregated storage metrics.
	Capacity(ctx context.Context) (domain.StorageInfo, error)
}

// AdminStore is the administrative API. It is required by the Store
// owner: the code that called InitStore / OpenStore. It is not
// passed to decorators or Curator.
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
	//
	// Not yet wired: returns errs.ErrNotImplemented in M2. The
	// implementation lands with the configuration-history work in
	// M3.x — by then a new system.config artifact is written, the
	// pointer in system.config/current is bumped atomically, and
	// the active config in memory swaps without restart.
	UpdateConfig(ctx context.Context, cfg domain.StoreConfig) error

	// Config returns a snapshot of the active StoreConfig — the
	// projection persisted in system.config/current, with defaults
	// applied. Read-only; mutations of the returned value have no
	// effect on the running Store.
	Config() domain.StoreConfig

	// ConfigHistory returns the full history of configuration
	// versions.
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

	// System returns the facade for engine-internal service
	// artifacts (configuration, agent cursors, index snapshots,
	// etc.). Reached only through AdminStore, so DataStore
	// consumers cannot see system state. See ADR-57 and
	// docs/3 Reference/01 Core/01 Types.md.
	System() SystemStore
}

// Store is the union of DataStore and AdminStore. Returned by
// InitStore and OpenStore. By passing only DataStore (and not Store)
// to a decorator or Curator, the host application guarantees at the
// type level that the administrative methods are unreachable from
// that code.
type Store interface {
	DataStore
	AdminStore
}
