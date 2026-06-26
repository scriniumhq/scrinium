// path — the driver-side path layout for blobs and manifests.
//
// Pure functions, no I/O. The conventions here are part of the on-disk
// format: changing them requires a migration. Canonical layouts:
//
//	Sharded:   blobs/<aa>/<bb>/<ref>   (aa,bb = hex chars 1..4 of the ref)
//	Flat:      blobs/<full-ref>
//
// Chunk and pack blobs use the same topology with roots "chunks/" and
// "packs/"; the BlobType argument selects the root. Manifests live under
// "manifests/" and are always Sharded — even on object stores the manifest
// directory sees enough churn that two-level sharding pays off. A manifest
// file is named by its ManifestDigest (the hash of the file bytes), NOT by
// the floating ArtifactID (the handle); the index maps handle → digest.

// rootFor returns the directory prefix for a blob type: "blobs",
// "chunks", "packs". An empty type means Regular. Unknown types error —
// callers should validate first, but a defensive check is cheap.
package layout
