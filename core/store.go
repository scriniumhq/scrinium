package core

import (
	"context"
	"io"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver"
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
	Put(ctx context.Context, a domain.Artifact, opts PutOptions) (domain.ArtifactID, error)

	// PutBlob writes an anonymous blob without creating a manifest.
	// Not a client method: it is used by level-3 decorators
	// (chunker.Wrapper for writing anonymous chunks).
	PutBlob(ctx context.Context, r io.Reader, blobType BlobType) (domain.ContentHash, error)

	// Get opens an artifact for reading. It returns a ReadHandle —
	// a streaming primitive with lazy resolution of the physical
	// address.
	Get(ctx context.Context, id domain.ArtifactID, opts GetOptions) (ReadHandle, error)

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
	RollbackSession(ctx context.Context, sessionID string) error

	// Iteration and introspection.

	// Walk iterates over user manifests. namespace = "*" — every user
	// namespace; an empty string — only the default one.
	// system.* is unreachable through Walk.
	Walk(ctx context.Context, namespace string, cb func(domain.Manifest) error) error

	// WalkSystem iterates over system manifests (the system.*
	// namespace). It requires a CapabilityToken.
	WalkSystem(ctx context.Context, namespace string, cb func(domain.Manifest) error) error

	// Capacity returns aggregated storage metrics.
	Capacity(ctx context.Context) (StorageInfo, error)
}

// AdminStore is the administrative API. It is required by the Store
// owner: the code that called InitStore / OpenStore. It is not
// passed to decorators or Curator.
type AdminStore interface {
	// State returns the current state of the Store.
	State() StoreState

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
	// is not rewritten. After RotateKEK the previous Recovery Kit is
	// invalid; the host is required to obtain a new one through
	// ExportRecoveryKit.
	RotateKEK(ctx context.Context) error

	// SetMaintenanceMode transitions the Store into a maintenance
	// mode. Used before running a Maintenance Agent.
	SetMaintenanceMode(ctx context.Context, mode MaintenanceMode) error

	// UpdateConfig updates the mutable parameters of StoreConfig.
	// Immutable parameters cannot be changed — ErrConfigMismatch.
	UpdateConfig(ctx context.Context, cfg domain.StoreConfig) error

	// ConfigHistory returns the full history of configuration
	// versions.
	ConfigHistory(ctx context.Context) ([]domain.StoreConfig, error)
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
