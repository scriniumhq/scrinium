package domain

import "strings"

// CryptoIdentity is the part of a blob's identity that comes from
// HOW it is encrypted, over and above WHAT it contains. Empty for
// an unencrypted (Plain) blob; "<algorithm>/<KeyID>" for an
// encrypted one. It is the third component of the blob dedup key
// (ContentHash, OriginalSize, CryptoIdentity) — see ADR-58.
//
// The string form is deterministic and stable on disk: it is
// persisted in the index and must hash/compare identically whether
// computed by the engine at write time or reconstructed by
// RebuildIndexAgent from a manifest. Do not reorder or reformat.
type CryptoIdentity string

// CryptoIdentityOf derives the crypto-identity from a blob's
// Pipeline stages. A blob is encrypted iff one of its stages is a
// crypto algorithm; that stage's Algorithm and KeyID define the
// identity. Keyless stages (compression) never contribute — this is
// exactly the T-07 boundary ADR-58 draws: dedup stays independent
// of keyless Pipeline configuration, but crypto (key + IV) enters
// the key.
//
// Conventionally a blob has at most one crypto stage; if several
// were ever chained, the LAST one (closest to the bytes on disk)
// determines the identity, since it produced the final stream.
func CryptoIdentityOf(stages []PipelineStage) CryptoIdentity {
	id := CryptoIdentity("")
	for _, s := range stages {
		if IsCryptoAlgorithm(s.Algorithm) {
			id = CryptoIdentity(s.Algorithm + "/" + s.KeyID)
		}
	}
	return id
}

// IsCryptoAlgorithm reports whether a Pipeline algorithm id denotes
// a keyed (encrypting) transform. Compression and other keyless
// stages return false. Kept as an explicit allow-list rather than a
// registry lookup so domain stays dependency-free; crypto plugins
// must register under these canonical ids (3. Reference/04 §4.3).
func IsCryptoAlgorithm(algo string) bool {
	switch strings.ToLower(algo) {
	case "aes-gcm", "chacha20-poly1305":
		return true
	default:
		return false
	}
}

// BlobDedupKey is the full deduplication key of a blob, shared by
// every dedup layer (ADR-58): regular blobs (ExistsByContent),
// chunks (ExistsByHash), packs (packed_blobs), and cross-store
// (MultistoreIndex.ExistsAny). Two blobs collapse to one physical
// object only when all three components match.
//
//   - ContentHash    — the plaintext content hash (the global key).
//   - OriginalSize   — guards against hash-prefix collisions across
//     files of different lengths.
//   - CryptoIdentity — empty for Plain blobs; "<algorithm>/<KeyID>"
//     for encrypted ones, so a different key (or Plain vs encrypted)
//     never deduplicates.
//
// All three fields are comparable, so BlobDedupKey is usable directly
// as a Go map key (MultistoreIndex.ExistsAny returns a presence map
// keyed by it).
type BlobDedupKey struct {
	ContentHash    ContentHash
	OriginalSize   int64
	CryptoIdentity CryptoIdentity
}

// BlobDedupKeyOf assembles the dedup key for a blob from its
// plaintext ContentHash, OriginalSize and Pipeline stages. The
// crypto-identity is derived the same way CryptoIdentityOf does, so
// every layer that has a blob's stages can produce an identical key
// without duplicating the derivation logic.
func BlobDedupKeyOf(hash ContentHash, originalSize int64, stages []PipelineStage) BlobDedupKey {
	return BlobDedupKey{
		ContentHash:    hash,
		OriginalSize:   originalSize,
		CryptoIdentity: CryptoIdentityOf(stages),
	}
}
