// Package multistore is the aggregating index of the StoreSet plane
// (ADR-91, ADR-43): a resolver over several per-store index.StoreIndex.
//
// It is a NATIVE part of the multistore plane — not an Extension and not a
// wrapper. Eventually consistent by nature: fully derivable from the
// physical state of the member StoreIndexes, so a lost instance (restart,
// reconfiguration) is rebuilt by warming over each registered StoreIndex
// (a shared Walk, ADR-91).
//
// STATUS: architectural skeleton. The contract (MultistoreIndex) is final
// and matches "3. Reference/03 StoreIndex §3.9". Method bodies are stubs
// (return ErrNotImplemented) pending implementation. A Postgres-backed
// MultistoreIndex (a VIEW over a shared DB) is a separate forward impl in
// index/postgres (postgres.NewMultistore), not this package.
package multistore

import (
	"context"
	"errors"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index"
)

// ErrNotImplemented marks a skeleton method whose body is not yet written.
var ErrNotImplemented = errors.New("multistore: not implemented (skeleton)")

// MultistoreIndex aggregates several per-store index.StoreIndex into one
// resolver for the StoreSet plane. Explicit mutation contract
// (RegisterArtifact/MarkStale/PruneStale): the plane drives it; nested
// Stores do not know about it (encapsulation, ADR-43).
type MultistoreIndex interface {
	// ResolveArtifact returns every Store holding the artifact (Target +
	// Backups), sorted by Priority (see 01 Core/06 Multistore). Empty
	// slice means the artifact is in no Store; Get returns ErrArtifactNotFound.
	ResolveArtifact(ctx context.Context, id domain.ArtifactID) ([]domain.StoreID, error)

	// ExistsAny batch-checks blob presence in ANY member Store by dedup
	// key: for each key, whether at least one Store holds that blob. Used
	// by the Ingester to optimise bulk import. Distinct from
	// StoreIndex.ExistsByContent (which is the exact, single-store check
	// returning a concrete blob_ref).
	ExistsAny(ctx context.Context, keys []domain.BlobDedupKey) (map[domain.BlobDedupKey]bool, error)

	// RegisterArtifact records an artifact in the aggregate. Called by the
	// StoreSet plane, never by a nested Store. An artifact in the transit
	// store is registered as living in the transit store (a full Store);
	// after packing+delivery the record is updated to the destination Store.
	RegisterArtifact(ctx context.Context, id domain.ArtifactID, storeID domain.StoreID, key domain.BlobDedupKey) error

	// MarkStale flags a route stale on a read-miss (Read-Repair, ADR-43):
	// the next ResolveArtifact will not return that Store. Called on the
	// read-path when Driver.Get yields os.ErrNotExist for a route the
	// aggregate still lists.
	MarkStale(ctx context.Context, id domain.ArtifactID) error

	// PruneStale periodically removes flagged routes; live routes are left
	// untouched.
	PruneStale(ctx context.Context) error
}

// NewMemoryIndex builds an in-memory MultistoreIndex over the given member
// StoreIndexes (members may be SQLite on separate disks). It warms its
// routes by walking each member once; on loss it is rebuilt the same way.
//
// SKELETON: the constructor wires members; warm-up and the method bodies
// are not yet implemented (return ErrNotImplemented).
func NewMemoryIndex(members ...index.StoreIndex) MultistoreIndex {
	return &memoryIndex{members: members}
}

type memoryIndex struct {
	members []index.StoreIndex
	// TODO(multistore): derived state, warmed from members and rebuilt on
	// loss (ADR-91): route map (ArtifactID -> []StoreID), a dedup presence
	// view keyed by BlobDedupKey, and a stale-route set for Read-Repair.
}

func (m *memoryIndex) ResolveArtifact(ctx context.Context, id domain.ArtifactID) ([]domain.StoreID, error) {
	// TODO(multistore): aggregate routes across members, sort by Priority.
	return nil, ErrNotImplemented
}

func (m *memoryIndex) ExistsAny(ctx context.Context, keys []domain.BlobDedupKey) (map[domain.BlobDedupKey]bool, error) {
	// TODO(multistore): batch existence across members (ADR-10 batching).
	return nil, ErrNotImplemented
}

func (m *memoryIndex) RegisterArtifact(ctx context.Context, id domain.ArtifactID, storeID domain.StoreID, key domain.BlobDedupKey) error {
	// TODO(multistore): record route; best-effort/async per ADR-43 T-04.
	return ErrNotImplemented
}

func (m *memoryIndex) MarkStale(ctx context.Context, id domain.ArtifactID) error {
	// TODO(multistore): Read-Repair invalidation.
	return ErrNotImplemented
}

func (m *memoryIndex) PruneStale(ctx context.Context) error {
	// TODO(multistore): drop flagged routes.
	return ErrNotImplemented
}

// Interface guard: the skeleton impl satisfies the contract.
var _ MultistoreIndex = (*memoryIndex)(nil)
