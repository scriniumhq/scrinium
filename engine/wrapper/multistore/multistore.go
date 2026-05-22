package multistore

import (
	"context"

	"scrinium.dev/engine/domain"
)

// MultistoreIndex is the aggregating index at the multistore
// level. A wrapper over several domain.StoreIndexes; needed only
// when there are multiple Stores. With a single Store, callers
// work with the StoreIndex directly.
//
// Eventually consistent by nature — fully derivable from the
// physical state of the underlying StoreIndexes.
//
// ctx threading on the point methods is added in chunk R9 (F-110);
// only PruneStale carries a ctx today.
type MultistoreIndex interface {
	// ResolveArtifact returns the list of Stores in which the
	// artifact is registered. Used when reading through Curator.
	ResolveArtifact(id domain.ArtifactID) ([]domain.StoreID, error)

	// ExistsAny is a batch presence check across every Store, keyed
	// by the full dedup key (ADR-58). It carries the same
	// BlobDedupKey triple the single-store probes use, so cross-store
	// dedup honours crypto-identity: a different KeyID (or Plain vs
	// encrypted) never matches, and a cross-store Convergent blob
	// matches only on identical content under the same key. Returns a
	// presence map keyed by the queried key. Used by the Ingester to
	// aggregate requests before physical writes.
	ExistsAny(keys []domain.BlobDedupKey) (map[domain.BlobDedupKey]bool, error)

	// RegisterArtifact records that an artifact is present in a
	// given Store, together with its blob dedup key so cross-store
	// resolution carries the crypto-identity (ADR-58). Called by
	// Curator after a successful write or Drain.
	RegisterArtifact(id domain.ArtifactID, storeID domain.StoreID, key domain.BlobDedupKey) error

	// MarkStale marks a record as stale (Read-Repair on a cache
	// miss: the index has a route but the artifact is physically
	// missing from the Location).
	MarkStale(id domain.ArtifactID) error

	// PruneStale periodically clears stale records. May be invoked
	// in the background or on demand.
	PruneStale(ctx context.Context) error
}
