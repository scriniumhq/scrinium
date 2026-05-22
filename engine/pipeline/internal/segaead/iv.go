package segaead

// iv.go — per-segment IV derivation (ADR-59 §"Вывод IV сегмента",
// docs/2. Internals/03 §3.2.1). The segment index always enters the
// derivation so no two segments of one blob can share an IV, even
// when their plaintext is identical.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// randomIV returns a fresh crypto/rand IV. Used in IVModeRandom
// (EncryptedDedup Disabled): the IV is already unique, and the
// segment index in the frame layout is the format-level backstop.
func randomIV() ([]byte, error) {
	iv := make([]byte, IVLen)
	if _, err := rand.Read(iv); err != nil {
		return nil, fmt.Errorf("segaead: random IV: %w", err)
	}
	return iv, nil
}

// convergentIV derives a deterministic IV for one segment:
//
//	IV = HMAC-SHA256( DEK, SHA-256(plaintext) ‖ KeyID ‖ uint64(index) )[:12]
//
// The HMAC key is the DEK (not the KeyID): this binds the IV to the
// key material, so the ADR-58 invariant "different KeyID → different
// blob" holds mechanically (a different DEK yields a different IV,
// hence different ciphertext and a different BlobRef).
//
// The inner segment hash is SHA-256 unconditionally — not the
// store's ContentHasher. The mechanic is self-contained: HMAC-SHA256
// already pulls in crypto/sha256, the digest is a fixed-size 12-byte
// squeeze (HKDF would be overkill, ADR-59), and reproducibility of
// the ciphertext depends only on (DEK, KeyID, SegmentSize, segment
// bytes) — never on which content-hash family the store chose for
// its dedup key. Cross-store convergent dedup therefore needs
// matching DEK/KeyID/SegmentSize (ADR-58), independent of this hash.
func convergentIV(dek, keyID []byte, plaintext []byte, index uint64) []byte {
	segHash := sha256.Sum256(plaintext)

	var idx [8]byte
	binary.BigEndian.PutUint64(idx[:], index)

	mac := hmac.New(sha256.New, dek)
	mac.Write(segHash[:])
	mac.Write(keyID)
	mac.Write(idx[:])
	return mac.Sum(nil)[:IVLen]
}
