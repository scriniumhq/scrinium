package core

import (
	"context"
	"time"

	"github.com/rkurbatov/scrinium/domain"
)

// StoreIndex is the index of a single Store. Every mutating method
// encapsulates its transaction inside; the calling code never
// drives transactions explicitly.
//
// Implementations (in-memory, sqlite, postgres) live in subpackages
// of index/.
type StoreIndex interface {
	// Writes and deletes.

	// IndexManifest registers an artifact in the index. It branches
	// on manifest.Type:
	//   - blob: upsert blob, increment ref_count, insert manifest.
	//   - toc:  + increment ref_count for each chunkRef.
	//   - pack: transitive registration of every packed artifact via
	//     packedEntries (see docs/2. Internals/09 §9.2.1).
	IndexManifest(
		m domain.Manifest,
		addr domain.PhysicalAddress,
		chunkRefs []string,
		packedEntries []domain.PackedEntry,
	) error

	// DeleteManifest performs a logical deletion: a single
	// transaction, DELETE manifest + decrement ref_count for each
	// blobRef.
	DeleteManifest(artifactID domain.ArtifactID, blobRefs []string) error

	// RebindBlob moves a blob from Workspace: Host to
	// Workspace: Location after a successful Drain. ref_count is
	// not changed. Idempotent: a no-op when the record is missing.
	RebindBlob(ctx context.Context, blobRef string, newAddr domain.PhysicalAddress) error

	// Resolution and existence checks.

	// Resolve returns the physical address for a BlobRef.
	Resolve(blobRef string) (domain.PhysicalAddress, error)

	// ExistsByContent is an exact check by the composite key
	// (ContentHash, OriginalSize). The deduplication key for regular
	// blobs.
	ExistsByContent(hash domain.ContentHash, originalSize int64) (blobRef string, exists bool, err error)

	// ExistsByHash is the check by ContentHash with tombstone
	// distinction. Used by chunker.Wrapper for chunk deduplication.
	ExistsByHash(hash domain.ContentHash) (domain.BlobExistStatus, error)

	// GetRefCount returns the current reference count of a blob.
	GetRefCount(blobRef string) (int, error)

	// LookupPacked returns the data needed for a range read by the
	// ArtifactID of a packed artifact. The second return value is
	// false when the artifact is not packed (it lives in /blobs/ or
	// /manifests/ as usual).
	LookupPacked(artifactID domain.ArtifactID) (domain.PackedBlobInfo, bool, error)

	// ManifestExists reports whether a manifest row with the given
	// ArtifactID is present in the index. It is the manifests-side
	// counterpart of Resolve: a point-lookup that does not return
	// the row contents, only its presence. Used by the bootstrap
	// Orphan Scan to find manifest files on disk that have no
	// matching index row (the crash window between Driver.Put on
	// the manifest path and the IndexManifest transaction).
	//
	// A false return with a nil error is the normal "not present"
	// signal. Errors are reserved for index-infrastructure
	// failures.
	ManifestExists(id domain.ArtifactID) (bool, error)

	// Iteration. Implementations are required to stream through the
	// callback rather than load the whole result set into memory.

	// ListByNamespace iterates over manifests with the given
	// namespace. "*" — all namespaces; "" — only the default
	// (empty). Returns blob and toc; pack is excluded.
	ListByNamespace(ctx context.Context, ns string, cb func(domain.Manifest) error) error

	// ListOrphanBlobs iterates over blobs with ref_count = 0. Used
	// by the GC Agent.
	ListOrphanBlobs(ctx context.Context, cb func(blobRef string) error) error

	// ListUnverified iterates over blobs whose last_verified_at is
	// older than `before`. Used by the Scrub Agent.
	ListUnverified(ctx context.Context, before time.Time, cb func(blobRef string) error) error

	// GetBySession returns every ArtifactID with the given
	// SessionID. Used by RollbackSession.
	GetBySession(sessionID string) ([]domain.ArtifactID, error)

	// Verification and maintenance.

	// MarkVerified updates last_verified_at for a blob.
	MarkVerified(blobRef string, timestamp time.Time) error

	// DeletePacked removes every packed_blobs record of a given
	// pack volume. Called by the GC Agent before tombstoning the
	// pack blob.
	DeletePacked(packBlobRef string) error

	// VacuumInto creates a snapshot of the index at the given path.
	// Used by the Snapshot Agent.
	VacuumInto(ctx context.Context, destPath string) error

	// store_meta service table. A singleton key/value store for
	// Store metadata: schema_version, descriptor cache,
	// last_orphan_scan_at, etc.

	GetMeta(key string) (string, error)
	SetMeta(key string, value string) error
}
