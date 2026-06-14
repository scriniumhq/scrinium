package manifestfx

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/domain/fsmeta"
)

// syntheticDigest derives a stable, valid-shaped ManifestDigest from an
// artifact id. After the identity axis (ADR-83/92) the manifests table
// is keyed by manifest_digest, so two fixtures must carry two distinct
// digests to land two rows — distinct ids give distinct digests, while
// the same id re-used gives the same digest (idempotent re-index). The
// index treats the digest as an opaque PK; the production write path
// computes the real digest via artifact.ComputeManifestDigest, so this
// fixture value never reaches the store.
func syntheticDigest(id string) domain.ManifestDigest {
	sum := sha256.Sum256([]byte("manifestfx:" + id))
	return domain.ManifestDigest(hex.EncodeToString(sum[:]))
}

// Synthetic hashes used in fixtures. Cannot be const because
// strings.Repeat is a runtime call.
var (
	contentHashAaa = strings.Repeat("a", 64)
	blobRefBbb     = strings.Repeat("b", 64)
)

// Sample returns a minimal valid blob manifest with a fixed
// CreatedAt — byte-stable across runs for round-trip tests.
func Sample() domain.Manifest {
	return domain.Manifest{
		Namespace:    "users",
		SessionID:    "sess-1",
		CreatedAt:    time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
		ContentHash:  domain.ContentHash(contentHashAaa),
		OriginalSize: 4096,
		BlobRefs:     []domain.BlobRef{domain.BlobRef(blobRefBbb)},
		LayoutHeader: domain.LayoutHeader{BlobStorage: domain.LayoutTarget},
	}
}

// Blob returns a small blob manifest with caller-supplied id and
// blobRef. ContentHash and OriginalSize are fixed — two manifests
// produced by Blob with different blobRef will collide on the
// (content_hash, original_size) UNIQUE index of the StoreIndex
// schema. Use this when:
//
//   - you only need one blob in the test;
//   - you want the dedup case (same blobRef passed twice or for
//     two different artifact IDs — bumps ref_count, no second row).
//
// For tests that need several distinct blobs, use BlobWithHash.
//
// CreatedAt is time.Now() — fine for index tests that don't check
// byte stability.
func Blob(id, blobRef string) domain.Manifest {
	return domain.Manifest{
		ArtifactID:   domain.ArtifactID(id),
		Digest:       syntheticDigest(id),
		Namespace:    "test",
		ContentHash:  domain.ContentHash(contentHashAaa),
		BlobRefs:     []domain.BlobRef{domain.BlobRef(blobRef)},
		OriginalSize: 1024,
		CreatedAt:    time.Now(),
	}
}

// BlobWithHash is Blob with a caller-controlled ContentHash and
// OriginalSize. Use whenever a test stages two or more independent
// blobs in the same index — distinct contentHash values keep the
// (content_hash, original_size) UNIQUE constraint happy.
//
// Convention for synthetic hashes: SyntheticHash('a') returns the
// canonical 64-char "a"-padded sha256-prefixed string.
func BlobWithHash(id, blobRef string, contentHash domain.ContentHash, originalSize int64) domain.Manifest {
	return domain.Manifest{
		ArtifactID:   domain.ArtifactID(id),
		Digest:       syntheticDigest(id),
		Namespace:    "test",
		ContentHash:  contentHash,
		BlobRefs:     []domain.BlobRef{domain.BlobRef(blobRef)},
		OriginalSize: originalSize,
		CreatedAt:    time.Now(),
	}
}

// EncryptedBlobWithHash is BlobWithHash plus a single crypto
// Pipeline stage, so the index derives a non-empty crypto-identity
// ("<algo>/<keyID>") for the blob row. For ADR-58 dedup-key tests.
func EncryptedBlobWithHash(id, blobRef string, contentHash domain.ContentHash, originalSize int64, algo, keyID string) domain.Manifest {
	m := BlobWithHash(id, blobRef, contentHash, originalSize)
	m.Pipeline = []domain.PipelineStage{{Algorithm: algo, KeyID: keyID}}
	return m
}

// SyntheticHash builds a 64-hex-character bare hash (ADR-93) by
// repeating fillChar. Convenient when staging multiple blobs that
// must have distinct content hashes:
//
//	SyntheticHash('a') == "aaaa…aaaa"
//	SyntheticHash('b') == "bbbb…bbbb"
//
// fillChar must be a hex digit (0-9, a-f); the function does not
// validate — passing a non-hex character produces a string the
// hash registry will refuse to parse, which is the desired
// failure for a test feeding malformed input.
func SyntheticHash(fillChar byte) domain.ContentHash {
	return domain.ContentHash(strings.Repeat(string(fillChar), 64))
}

// PhysAddr is a Location-workspace address at path.
func PhysAddr(path string) domain.PhysicalAddress {
	return domain.PhysicalAddress{
		Path: path,
	}
}

// ManifestWithFsmetaPath builds a Blob manifest tagged with an fsmeta
// virtual path — the common fixture for projection and surface tests
// that need an artifact placed at a known path in the by-path tree.
func ManifestWithFsmetaPath(id, path string) domain.Manifest {
	m := Blob(id, strings.Repeat("b", 64))
	if err := AddFsmetaPath(&m, path); err != nil {
		panic("manifestfx.ManifestWithFsmetaPath: " + err.Error())
	}
	return m
}

// AddFsmetaPath sets the manifest's Ext block to an fsmeta record
// carrying path. Overwrites any existing Ext.
func AddFsmetaPath(m *domain.Manifest, path string) error {
	raw, err := fsmeta.Encode(fsmeta.FileSystem{Path: path})
	if err != nil {
		return err
	}
	m.Ext = raw
	return nil
}
