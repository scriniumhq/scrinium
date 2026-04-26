package domain

// ArtifactID is the public identifier of an Artifact. It is a
// cryptographic hash of the final serialised manifest file (header
// included). Format: "<algo>-<hex>" (for example,
// "sha256-abc..."). Any change to the metadata produces a new
// Manifest and a new ArtifactID.
type ArtifactID string

// ContentHash is the hash of the original payload before any
// transformation. The global deduplication key: two files with the
// same content share a ContentHash regardless of Pipeline
// configuration.
type ContentHash string

// BlobRef is the hash of the final transformed blob stream (after
// compression and encryption). Used as the physical filename when
// blobs are stored individually. Applies to all blobs, including
// chunks and TOC blobs.
type BlobRef string

// StoreID is the global identifier of a Store. A UUID v4, generated
// once at InitStore; never changes.
type StoreID string

// ContentHashAlgorithm identifies a content-hashing algorithm.
// An immutable Store parameter: changing it breaks deduplication
// and verification of historical artifacts.
type ContentHashAlgorithm string

const (
	HashSHA256 ContentHashAlgorithm = "sha256"
	HashBLAKE3 ContentHashAlgorithm = "blake3"
)
