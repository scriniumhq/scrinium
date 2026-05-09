package curator

import (
	"context"

	"github.com/rkurbatov/scrinium/engine/domain"
)

// MultistoreIndex is the aggregating index at the Curator level.
// A wrapper over several domain.StoreIndexes; needed only when there
// are multiple Stores. With a single Store, Curator works with the
// StoreIndex directly.
//
// Eventually consistent by nature — fully derivable from the
// physical state of the underlying StoreIndexes.
type MultistoreIndex interface {
	// ResolveArtifact returns the list of Stores in which the
	// artifact is registered. Used when reading through Curator.
	ResolveArtifact(id domain.ArtifactID) ([]domain.StoreID, error)

	// ExistsAny is a batch presence check across every Store.
	// Used by the Ingester to aggregate requests before physical
	// writes. Without OriginalSize: an exact composite-key check is
	// excessive for a batch optimisation.
	ExistsAny(hashes []domain.ContentHash) (map[domain.ContentHash]bool, error)

	// RegisterArtifact records that an artifact is present in a
	// given Store. Called by Curator after a successful write or
	// Drain.
	RegisterArtifact(id domain.ArtifactID, storeID domain.StoreID, hash domain.ContentHash) error

	// MarkStale marks a record as stale (Read-Repair on a cache
	// miss: the index has a route but the artifact is physically
	// missing from the Location).
	MarkStale(id domain.ArtifactID) error

	// PruneStale periodically clears stale records. May be invoked
	// in the background or on demand.
	PruneStale(ctx context.Context) error
}
