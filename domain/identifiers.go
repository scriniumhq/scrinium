package domain

import "github.com/google/uuid"

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

// SessionID identifies a logical batch of operations grouped under
// one mount or one ingest session. Scoped artifacts share a
// SessionID so RollbackSession can rewind the entire batch
// atomically.
//
// Use NewMountSessionID for mount-time sessions; explicit
// SessionID("ingest-batch-1") conversion is appropriate for batches
// where the host wants a meaningful name.
//
// The newtype distinguishes session identifiers from other strings
// at the type level — no more passing a Namespace string by mistake
// where a SessionID was expected.
type SessionID string

// NewMountSessionID generates a fresh SessionID with the "mount-"
// prefix and a UUID v4 suffix. Used during store assembly and
// init so every runtime gets a unique mount-scoped
// identifier without callers reaching for uuid.NewString themselves.
func NewMountSessionID() SessionID {
	return SessionID("mount-" + uuid.NewString())
}

// ContentHashAlgorithm identifies a content-hashing algorithm.
// An immutable Store parameter: changing it breaks deduplication
// and verification of historical artifacts.
type ContentHashAlgorithm string

const (
	HashSHA256 ContentHashAlgorithm = "sha256"
	HashBLAKE3 ContentHashAlgorithm = "blake3"
)
