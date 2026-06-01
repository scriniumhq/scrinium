// Package source defines the contracts a projection reads from: the
// artifact Provider, the optional Ext metadata provider, the path
// Resolver a host plugs in to map manifests onto a tree, and the Kind
// of backing store. It is a leaf — only domain and engine/store — so
// the view, the resolvers (fsmeta), and the index extensions
// (fsindex) depend on these contracts without depending on each
// other.
package source

import (
	"context"
	"encoding/json"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
)

// Provider is the minimal contract a view reads artifacts from: walk
// the manifests of a namespace, and fetch an artifact's bytes by id.
// The dependency is inverted — the projection layer owns this
// interface; the store implements it.
type Provider interface {
	Walk(ctx context.Context, namespace string, cb func(domain.Manifest) error) error
	Get(ctx context.Context, id domain.ArtifactID, opts ...store.GetOption) (domain.ReadHandle, error)
}

// Ext is the optional bulk metadata provider a view's backfill uses
// to fetch an artifact's extension block in one round-trip instead of
// re-reading the manifest.
//
// Ext returns (raw, true, nil) when found, (nil, false, nil) when the
// artifact has no ext block, and a non-nil error only for
// infrastructure failures (DB I/O).
type Ext interface {
	Ext(id domain.ArtifactID) (json.RawMessage, bool, error)
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
