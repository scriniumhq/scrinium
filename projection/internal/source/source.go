package source

import (
	"context"
	"encoding/json"

	"scrinium.dev/domain"
)

// Provider is the minimal contract a view reads artifacts from: walk
// the manifests of a namespace, and fetch an artifact's bytes by id.
// The dependency is inverted — the projection layer owns this
// interface; the store implements it.
type Provider interface {
	Walk(ctx context.Context, cb func(domain.Manifest) error) error
	Get(ctx context.Context, id domain.ArtifactID, opts ...domain.GetOption) (domain.ReadHandle, error)
}

// Metadata is the optional bulk metadata provider a view's backfill uses
// to fetch an artifact's custom index block in one round-trip instead of
// re-reading the manifest.
//
// Metadata returns (raw, true, nil) when found, (nil, false, nil) when the
// artifact has no ext block, and a non-nil error only for
// infrastructure failures (DB I/O).
type Metadata interface {
	Metadata(id domain.ArtifactID) (json.RawMessage, bool, error)
}

// Resolver extracts a virtual path from a manifest. Implementing it
// is how a host plugs a metadata schema into the projection.
//
// Returns (path, true) when the artifact carries a recognised schema
// and a valid path; ("", false) when the artifact is opaque to this
// resolver. Pure: the same Manifest must always produce the same
// result — the view caches the decision.
type Resolver func(m domain.Manifest) (path string, ok bool)

// Kind labels the type of backing store a view was built from.
type Kind string

const (
	// KindStore — a single store.DataStore. StorageFacet is always
	// nil.
	KindStore Kind = "store"

	// KindMultistore — a multistore with a MultistoreIndex.
	// StorageFacet is populated.
	KindMultistore Kind = "multistore"
)
