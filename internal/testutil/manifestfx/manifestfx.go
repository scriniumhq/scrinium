package manifestfx

import (
	"strings"
	"time"

	"scrinium.dev/domain"
)

// Synthetic hashes used in fixtures. Cannot be const because
// strings.Repeat is a runtime call.
var (
	contentHashAaa = "sha256-" + strings.Repeat("a", 64)
	blobRefBbb     = "sha256-" + strings.Repeat("b", 64)
)

// Sample returns a minimal valid blob manifest with a fixed
// CreatedAt — byte-stable across runs for round-trip tests.
func Sample() domain.Manifest {
	return domain.Manifest{
		Type:         domain.ManifestTypeBlob,
		Namespace:    "users",
		SessionID:    "sess-1",
		CreatedAt:    time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
		ContentHash:  domain.ContentHash(contentHashAaa),
		OriginalSize: 4096,
		BlobRef:      domain.BlobRef(blobRefBbb),
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
		Type:         domain.ManifestTypeBlob,
		Namespace:    "test",
		ContentHash:  domain.ContentHash(contentHashAaa),
		BlobRef:      domain.BlobRef(blobRef),
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
		Type:         domain.ManifestTypeBlob,
		Namespace:    "test",
		ContentHash:  contentHash,
		BlobRef:      domain.BlobRef(blobRef),
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

// SyntheticHash builds a 64-hex-character sha256-prefixed string by
// repeating fillChar. Convenient when staging multiple blobs that
// must have distinct content hashes:
//
//	SyntheticHash('a') == "sha256-aaaa…aaaa"
//	SyntheticHash('b') == "sha256-bbbb…bbbb"
//
// fillChar must be a hex digit (0-9, a-f); the function does not
// validate — passing a non-hex character produces a string the
// hash registry will refuse to parse, which is the desired
// failure for a test feeding malformed input.
func SyntheticHash(fillChar byte) domain.ContentHash {
	return domain.ContentHash("sha256-" + strings.Repeat(string(fillChar), 64))
}

// PhysAddr is a Location-workspace address at path.
func PhysAddr(path string) domain.PhysicalAddress {
	return domain.PhysicalAddress{
		Path: path,
	}
}

// PackedAddr is a Location-workspace address inside a pack volume:
// physical_path points at the pack file, and pack_ref/offset/size
// describe the byte range of the embedded blob.
func PackedAddr(packPath, packRef string, offset, size int64) domain.PhysicalAddress {
	return domain.PhysicalAddress{
		Path:    packPath,
		PackRef: packRef,
		Offset:  offset,
		Size:    size,
	}
}
