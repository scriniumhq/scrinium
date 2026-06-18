// Package storecfgfx supplies ready-made domain.StoreConfig values for
// tests.
//
// It exists so test code outside the engine/store tree — e.g. the
// engine/internal/artifactio I/O tests, which exercise the write path
// against a realistic config — can obtain one without importing the
// store-internal storeconfig package (internal visibility forbids it).
//
// The values are explicit literals rather than a call into
// storeconfig.ApplyDefaults, since testutil cannot import that internal
// package. They are test inputs, not a mirror of production policy: a
// test asserts I/O behaviour *given this config*, so the fixture only has
// to stay a valid config. If the plain-store defaults change in a way the
// I/O tests should track, update Plain here.
package storecfgfx

import (
	"time"

	"scrinium.dev/domain"
)

// Plain returns the configuration of a default plain (unencrypted) store:
// the value storeconfig.ApplyDefaults yields for an empty input. Sharded
// layout, target (non-inline) blobs, JSON manifests, SHA-256 content
// hashing, no encryption. The crypto-only fields (EncryptedDedup,
// SegmentSize) stay zero, as they do for a plain store.
func Plain() domain.StoreConfig {
	return domain.StoreConfig{
		PathTopology:         domain.PathTopologySharded,
		BlobStorage:          domain.BlobStorageTarget,
		ManifestEncoding:     domain.ManifestEncodingJSON,
		ManifestCrypto:       domain.ManifestCryptoPlain,
		ContentHasher:        domain.HashSHA256,
		VerifyOnRead:         domain.VerifyOnReadAuto,
		DeletionPolicy:       domain.DeletionPolicyFree,
		GCLeasePolicy:        domain.GCLeaseAuto,
		PackAlignment:        domain.PackAlignmentAuto,
		TombstoneGracePeriod: 24 * time.Hour,
	}
}
