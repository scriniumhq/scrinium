package domain

import "github.com/google/uuid"

// ArtifactID is the public, stable identity of an Artifact — a
// *floating handle*: ArtifactID = PRF(NK, cd ‖ md), where cd =
// H(content), md = H(canon(identity-meta)), and NK is the store's
// naming key (a public domain constant in Plain/Sealed, a secret
// naming key in Paranoid). Format: "<algo>-<hex>".
//
// The handle is what the outside world holds (business DB, pointers)
// and what Put returns. It is STABLE across form changes —
// repack, re-key with the same key, rebundle, layout change — and
// changes only when the content (cd) or the naming-key domain
// changes (e.g. crossing into Paranoid). Unlike ManifestDigest, it
// is serialised inside the manifest body (it is an input computed
// from cd‖md, not derivable from the file bytes).
type ArtifactID string

// ManifestDigest is the hash of the *full serialised manifest file*
// (header included). Format: "<algo>-<hex>". It is the physical
// on-disk filename and the form-verifier for a manifest: it CHANGES
// whenever the manifest is repacked. The index maps ArtifactID
// (handle) → current ManifestDigest. Distinct type from ArtifactID
// on purpose: the compiler then rejects passing a handle where a
// physical digest (filename, storage key) is required, and vice
// versa.
type ManifestDigest string

// ContentHash is the hash of the original payload before any
// transformation. The global deduplication key: two files with the
// same content share a ContentHash regardless of Pipeline
// configuration. Also serves as cd, the content input to ArtifactID.
type ContentHash string

// BlobRef is the hash of the final transformed blob stream (after
// compression and encryption). Used as the physical filename when
// blobs are stored individually. Applies to all blobs, including
// chunks and TOC blobs.
type BlobRef string

// HandleRef is a reference from one artifact to another — an edge in
// the content-addressed DAG (ADR-92). It carries the target artifact's
// floating handle, so it lives in the handle address space and converts
// to ArtifactID for resolution: ResolveManifest(domain.ArtifactID(ref)).
// Distinct type from ArtifactID on purpose — it marks "an edge to
// another artifact" versus "this artifact's own identity slot" — and is
// symmetric to BlobRef for the BlobRefs/HandleRefs reference arrays.
type HandleRef ArtifactID

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

// IdentityMode is an immutable Store property controlling whether
// identical content+identity-meta coalesce to one handle.
//
//   - IdentityModeUnique (default): a fresh per-Put nonce is mixed
//     into the handle, so every Put yields a distinct ArtifactID.
//     WithIdempotent() opts a single call back into coalescing.
//   - IdentityModeCoalesced (WORM archive): no nonce; the handle is
//     deterministic (PRF(NK, cd‖md)), so identical artifacts share
//     one ArtifactID. WithUnique() opts a single call back out.
//
// Coalescing implies WORM (no deletion): a deduplicated manifest is
// referenced by the outside world invisibly to the store, so refcount
// is impossible. Coalescing is forbidden in Paranoid (a deterministic
// handle would leak content equality).
type IdentityMode string

const (
	IdentityModeUnique    IdentityMode = "unique"
	IdentityModeCoalesced IdentityMode = "coalesced"
)
