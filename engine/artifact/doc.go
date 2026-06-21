// Package artifact is the binary-format library for Scrinium artifacts.
//
// It owns exactly one thing: how an artifact is represented on disk —
// the manifest file layout (header, body, crypto blocks), the artifact
// ID computation, the content-hash string form, and the driver-side
// path layout for blobs and manifests. It is a pure library: functions
// over bytes, paths, and domain values, with no I/O and no knowledge of
// the Driver, the StoreIndex, or the Pipeline runner.
//
// # Layering
//
// artifact is the bottom of the artifact stack. Everything that touches
// an outside entity is a wrapper above it:
//
//   - Driver I/O (staging, rename-commit, reading blob bytes) — store.
//   - StoreIndex (dedup probe, IndexManifest, Resolve) — store.
//   - Pipeline (forward transform on write, reverse on read) — store.
//
// store calls into artifact for the format (Encode/Decode/ComputeArtifactID,
// BlobPath/ManifestPath) and keeps the orchestration. This keeps the
// dependency graph one-way: artifact ← store, never the reverse, and lets
// the whole format be unit-tested on bytes without mocking any entity.
//
// # Dependencies
//
// artifact imports only domain (the shared value types) and the aead
// primitive (for Sealed/Paranoid manifest-body encryption). A HashRegistry
// is passed in by the caller rather than imported as a global, so artifact
// never reaches for engine-wide state.
//
// # On-disk format
//
// A manifest file is: a 5+ byte header (4-byte magic "\x00SC1", a 1-byte
// crypto flag, and for encrypted modes a KeyID) followed by the body. In Plain
// mode the body is deterministic JSON. In Sealed mode the ext / usr /
// inline_blob blocks are each an independent AEAD block (system fields stay
// plaintext). In Paranoid mode the entire body is a single AEAD block.
// The blob path layout (Sharded / Flat) and the manifests/ sharding are
// part of this format — changing them requires a migration.
package artifact
