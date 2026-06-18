// Package systemstore is the facade for engine-internal service
// artifacts: versioned configuration, agent cursors, index snapshots, and
// the like, each addressed by a slash-separated name rather than by
// content hash. These artifacts live outside the content-addressed index,
// in their own address space, and are invisible to the data-plane Walk.
//
// It is reached through the store's AdminStore.System(); it shares no
// mutable state with the store core (only the driver, the hash registry,
// the immutable ContentHasher, and a logger), which is why it lives in
// its own package rather than as a facet over the store core.
//
// The on-disk layout (versioned names, keep=0 cells, exclusive-create
// publishing, verify-on-read) lives in engine/internal/namedio (ADR-85).
package systemstore
