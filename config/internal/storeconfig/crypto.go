package storeconfig

import (
	"encoding/json"
	"fmt"
)

// Content protection and addressing parameters: how manifests are
// encrypted, whether encrypted blobs deduplicate, which hash addresses
// content, and the key-derivation cost.

// ManifestCrypto controls manifest protection. Immutable.
//
// On-disk byte representation (header crypto flag) is stable
// across rename history: Sealed is byte 0x01, Paranoid is 0x02.
// Old in-flight configs containing the previous names
// ("Sealed", "Paranoid") are accepted by UnmarshalJSON for
// backwards compatibility.
type ManifestCrypto string

const (
	ManifestCryptoPlain    ManifestCrypto = "Plain"
	ManifestCryptoSealed   ManifestCrypto = "Sealed"
	ManifestCryptoParanoid ManifestCrypto = "Paranoid"
)

// UnmarshalJSON accepts both the current ManifestCrypto values
// ("Plain", "Sealed", "Paranoid") and the pre-ADR-55 names
// ("Sealed", "Paranoid"). Old system.config artifacts
// written before the rename remain readable; newly-serialised
// configs always use the current names.
//
// The bridge lives on the value type rather than at the codec
// layer because StoreConfig is also marshalled through stock
// encoding/json in writeSystemConfig — a Custom Unmarshaller
// keeps the migration transparent to every caller.
//
// Sealed is mapped to Sealed; Paranoid is mapped to
// Paranoid. The mapping is one-way (read only) — writes always
// use the new names.
func (c *ManifestCrypto) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	switch s {
	case "":
		*c = ""
	case "Plain":
		*c = ManifestCryptoPlain
	case "Sealed":
		*c = ManifestCryptoSealed
	case "Paranoid":
		*c = ManifestCryptoParanoid
	default:
		return fmt.Errorf("config.ManifestCrypto: unknown value %q", s)
	}
	return nil
}

// EncryptedDedup controls deduplication of ENCRYPTED blobs. Immutable.
//
// It has no effect on Plain (unencrypted) blobs: their dedup key is
// always (ContentHash, OriginalSize) per ADR-29. For an encrypting
// store it governs whether two writes of the same plaintext under
// the same key can collapse to one physical blob. See ADR-58.
type EncryptedDedup string

const (
	// EncryptedDedupDisabled — random IV per write. The same
	// plaintext yields different ciphertext, a different BlobRef,
	// a different address: encrypted blobs never deduplicate. Full
	// AEAD semantics, no equality leak. Default for an encrypting
	// store.
	EncryptedDedupDisabled EncryptedDedup = "Disabled"
	// EncryptedDedupConvergent — IV = KDF(ContentHash, KeyID),
	// realised per-segment as HMAC-SHA256(DEK, segHash ‖ KeyID ‖
	// index) (ADR-59). One plaintext under one key yields one
	// ciphertext, one BlobRef: encrypted blobs deduplicate, at the
	// cost of leaking content equality to a storage observer. Wired
	// in R8 (ADR-58/59).
	EncryptedDedupConvergent EncryptedDedup = "Convergent"
)

// ContentHashAlgorithm identifies a content-hashing algorithm.
// An immutable Store parameter: changing it breaks deduplication
// and verification of historical artifacts.
type ContentHashAlgorithm string

const (
	HashSHA256 ContentHashAlgorithm = "sha256"
	HashBLAKE3 ContentHashAlgorithm = "blake3"
)

// KDFParams are the parameters for deriving a KEK.
type KDFParams struct {
	Time    uint32
	Memory  uint32
	Threads uint8
}
