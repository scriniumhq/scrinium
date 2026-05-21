package store

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
// everything else → system.state) lives in namespaceForName below.

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

// SystemPutOption is the option type for SystemStore.Put.
type SystemPutOption func(*systemPutOptions)

// systemPutOptions is the resolved options struct for a single Put
// call. Unexported because the only way to populate it is through
// SystemPutOption constructors.
type systemPutOptions struct {
	skipIndex bool
}

// WithoutIndex skips indexing the manifest in StoreIndex. Used for
// artifacts whose presence in the index would be either redundant
// or actively harmful — most notably index snapshots themselves
// (indexing a snapshot of the index inside the same index creates
// an asymmetry where the snapshot row points at a manifest that
// only exists after the snapshot was taken).
//
// Default (no option) indexes the artifact. This is the right
// choice for cursors and config — small, frequently-read
// artifacts where the index access path is cheaper than reading
// the manifest file twice (once for the pointer's referent, once
// for the artifact body).
func WithoutIndex() SystemPutOption {
	return func(o *systemPutOptions) { o.skipIndex = true }
}
