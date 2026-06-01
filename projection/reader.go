package projection

import (
	"scrinium.dev/domain"
	"scrinium.dev/projection/view"
)

// Re-exported read-result types. External consumers depend on these
// projection-level names rather than on the view package, which is an
// implementation detail of the projection layer.

// Reader is the read-only query surface of a projection: search,
// blob-relation and location lookups, plus a stats/source snapshot.
// It is the public face of the read side — daemons and tools depend
// on Reader instead of the concrete View, which keeps the View's
// mutation methods and tree internals out of the public API.
//
// Obtain one via Projection.Queries.
type Reader interface {
	// Search returns up to limit hits for the query.
	Search(query string, limit int) []view.SearchResult

	// RelatedByBlobRef returns artifacts sharing the given blob,
	// excluding the artifact named by exclude.
	RelatedByBlobRef(blobRef domain.BlobRef, exclude domain.ArtifactID) []view.RelatedArtifact

	// LookupLocations returns every tree-placement of an artifact.
	LookupLocations(id domain.ArtifactID) (view.Locations, bool)

	// StatsSnapshot returns a copy of the current counters.
	StatsSnapshot() view.Stats

	// SourceName returns the source kind (e.g. "store").
	SourceName() string
}

// Queries returns the read-only query surface of the projection, or
// nil for a nil or read-empty projection.
func (p *Projection) Queries() Reader {
	if p == nil || p.View == nil {
		return nil
	}
	return p.View
}
