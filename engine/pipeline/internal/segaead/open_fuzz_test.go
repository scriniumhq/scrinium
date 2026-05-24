package segaead

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"io"
	"testing"
)

// FuzzOpen hardens the segmented-AEAD reader against arbitrary blob
// bytes. Contract (TESTING.md category 1): Open followed by a full
// drain must never panic. Malformed, truncated, or tampered input must
// surface as an error, not a crash. This is our own framing and
// decryption parser over attacker-influenced bytes — the high-value
// place to fuzz (the underlying AES-GCM primitive is upstream's to
// fuzz, our segment framing is ours).
func FuzzOpen(f *testing.F) {
	key := key32(1)

	// Seed with a real sealed blob so mutation explores the actual
	// frame format, plus a few obviously-foreign inputs.
	if block, err := aes.NewCipher(key); err == nil {
		if g, err := cipher.NewGCM(block); err == nil {
			if r, err := Seal(bytes.NewReader([]byte("hello segaead fuzz target")),
				SealParams{AEAD: g, Mode: IVModeRandom, DEK: key, KeyID: "k", SegmentSize: 64}); err == nil {
				if blob, err := io.ReadAll(r); err == nil {
					f.Add(blob)
				}
			}
		}
	}
	f.Add([]byte{})
	f.Add([]byte("not a segaead blob"))
	f.Add(make([]byte, 64))

	f.Fuzz(func(t *testing.T, data []byte) {
		aead := mustAEAD(t, key32(1))
		rd, err := Open(bytes.NewReader(data), []cipher.AEAD{aead})
		if err != nil {
			return // rejected at Open — fine
		}
		// Draining may error (truncated/tampered); it must not panic.
		_, _ = io.ReadAll(rd)
	})
}
