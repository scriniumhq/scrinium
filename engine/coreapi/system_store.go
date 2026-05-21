package coreapi

// system_store.go — ADR-57 SystemStore facade.
//
// SystemStore is the engine-internal API for service artifacts:
// configuration, agent cursors, index snapshots, lease coordination.
// Reached through AdminStore.System(), so the access boundary is at
// the type level — DataStore cannot see it. Per ADR-57 the model is:
//
//   - Addressing by NAME, not ArtifactID. Each name maps to one
//     active artifact through a pointer file managed by the
//     SystemStore.
//   - Standard Pipeline (hashing, optional encryption). Two indexing
//     modes — default indexes the manifest in StoreIndex; WithoutIndex
//     skips it (for snapshots of the index itself, etc).
//   - Per-name atomic update: write new artifact → atomic-rename
//     pointer → drop predecessor. Crash between rename and drop
//     leaves an orphan that the bootstrap Orphan Scan reclaims
//     (docs/2 §10.2).
//
// The name → physical namespace mapping (config/* → system.config,
// everything else → system.state) lives in namespaceForName, in the
// core implementation.

import (
	"context"
	"io"

	"scrinium.dev/engine/domain"
)

// SystemStore — facade for engine-internal service artifacts. See
// the file header and ADR-57 for the model; documented contract in
// docs/3. Reference/01 Core/01 Types.md.
type SystemStore interface {
	// Put writes a system artifact under the given name. If the
	// name already has an artifact, the predecessor is dropped
	// after the pointer flip. The default is to index the manifest
	// in StoreIndex; WithoutIndex() skips indexing.
	Put(ctx context.Context, name string, payload io.Reader, opts ...SystemPutOption) error

	// Get opens the artifact currently pointed at by name. Returns
	// errs.ErrArtifactNotFound when no pointer exists.
	Get(ctx context.Context, name string) (ReadHandle, error)

	// Delete removes the pointer and the artifact it points at.
	// Idempotent: deleting an absent name returns nil.
	Delete(ctx context.Context, name string) error

	// Walk iterates over every name with the given prefix in
	// alphabetical order, yielding the underlying manifests.
	Walk(ctx context.Context, prefix string, cb func(name string, m domain.Manifest) error) error
}

// SystemPutConfig is the resolved set of options for a single
// SystemStore.Put. Public because it crosses the package boundary:
// the SystemStore contract lives here in coreapi, while option
// constructors (WithoutIndex) live in the core implementation and
// populate this struct through SystemPutOption.
type SystemPutConfig struct {
	// SkipIndex skips indexing the manifest in StoreIndex.
	SkipIndex bool
}

// SystemPutOption configures a single SystemStore.Put. An applier
// over SystemPutConfig rather than a func(*private) so the contract
// is self-contained in coreapi and option constructors can live in
// any package that implements it.
type SystemPutOption interface {
	ApplySystemPut(*SystemPutConfig)
}
