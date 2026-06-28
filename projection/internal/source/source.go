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

// Token is the opaque, monotonic change marker a TokenSource reports
// (ADR-106/107). The projection treats it as an opaque comparable value;
// the engine index issues it and the composition root adapts the backend's
// typed token onto this alias.
type Token = uint64

// TokenSource is the pull half of the synchronization seam (ADR-107): the
// View reads it to learn the backend's current change-sequence and, in later
// stages, to decide whether its cached trees are stale. It is a structural
// interface so the projection takes no dependency on engine/index — the
// composition root adapts index.SyncSource onto it.
//
// A nil TokenSource means snapshot semantics: the View reflects the backend
// as of New and does not observe other writers (INV-107-6).
type TokenSource interface {
	Token(ctx context.Context) (Token, error)
}

// Waiter is the optional push half (ADR-107): it blocks until the backend
// moves past `after` (or ctx is cancelled), letting the View refresh eagerly
// instead of polling. Structural, for the same reason as TokenSource; the
// composition root adapts index.SyncWaiter onto it. A View may hold a
// TokenSource without a Waiter — it then refreshes lazily on read.
type Waiter interface {
	Wait(ctx context.Context, after Token) (Token, error)
}

// Delta is a batch of resolved manifest changes for incremental convergence
// (ADR-107). Changes are the manifests added or updated since the cursor,
// already resolved to full domain.Manifest values (the composition root pairs
// the index's digest-level Since with its manifest resolver). Deletions are
// NOT enumerated — a hard delete prunes history, so Gapped is set and the View
// falls back to a full re-walk. Next is the cursor to store after applying.
type Delta struct {
	Changes []domain.Manifest
	Next    Token
	Gapped  bool
}

// DeltaSource is the incremental half of the pull seam (ADR-107): a
// TokenSource that can also enumerate the manifests changed since a cursor, so
// the View converges by upserting just those instead of re-walking the whole
// source. Structural, like TokenSource. by-assertion — the View uses it when
// the wired source implements it and falls back to a full re-derive
// (TokenSource alone) otherwise, so a backend that exposes only Token still
// works, just less cheaply.
type DeltaSource interface {
	TokenSource
	Since(ctx context.Context, cursor Token) (Delta, error)
}
