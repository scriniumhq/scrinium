package manifestcodec

// bodyaead.go — the manifest-body AEAD primitive (Sealed/Paranoid),
// folded in from the former internal/manifestcrypto package. It is
// the single-buffer, nonce-prefixed AES-GCM used to seal manifest
// body blocks; the streaming/segmented blob AEAD is a different
// mechanic and lives with the pipeline (pipeline/internal/segaead).
// Both share only the low-level cipher construction in internal/aead.
//
// These helpers are unexported: their only caller is encrypted.go in
// this package. The wire layout is nonce ‖ ciphertext ‖ tag.

import (
	"crypto/rand"
	"fmt"

	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/aead"
)

// minCiphertext is the smallest valid body ciphertext: a nonce prefix
// and a tag, with zero plaintext between.
const minCiphertext = aead.NonceLen + aead.TagLen

// sealBody encrypts plaintext with dek under AES-256-GCM, binding
// aadBytes to the ciphertext via the auth tag.
//
// Output layout: nonce (12 bytes, random) | ciphertext | tag (16 bytes).
//
// The nonce is generated fresh from crypto/rand on every call;
// callers must NOT reuse a sealBody output as input to a second call
// — that would be nonce reuse, which AES-GCM does not survive.
//
// Errors:
//   - len(dek) != aead.DEKLen → wrapped error (programmer mistake).
//   - crypto/rand failure → wrapped error (host RNG broken).
//   - cipher construction failure → wrapped error.
func sealBody(plaintext, dek, aadBytes []byte) ([]byte, error) {
	gcm, err := aead.NewGCM(dek)
	if err != nil {
		return nil, fmt.Errorf("manifestcodec.sealBody: %w", err)
	}

	nonce := make([]byte, aead.NonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("manifestcodec.sealBody: nonce: %w", err)
	}

	// gcm.Seal appends ciphertext+tag to dst; passing nonce as dst
	// lays the result out exactly as the wire format expects:
	// nonce ‖ ciphertext ‖ tag.
	out := gcm.Seal(nonce, nonce, plaintext, aadBytes)
	return out, nil
}

// openBody decrypts a ciphertext produced by sealBody. The nonce is
// read from the leading bytes; the trailing 16 bytes are the auth tag
// AES-GCM verifies before returning plaintext.
//
// Errors:
//   - len(dek) != aead.DEKLen → wrapped error.
//   - len(ciphertext) < minCiphertext → wrapped error (truncated data).
//   - tag mismatch (wrong DEK, modified ciphertext, or modified aad)
//     → errs.ErrDecryptionFailed. The three failure modes are
//     deliberately collapsed as a defence against side-channel oracles.
func openBody(ciphertext, dek, aadBytes []byte) ([]byte, error) {
	if len(ciphertext) < minCiphertext {
		return nil, fmt.Errorf("manifestcodec.openBody: ciphertext too short (%d bytes, need at least %d)",
			len(ciphertext), minCiphertext)
	}

	gcm, err := aead.NewGCM(dek)
	if err != nil {
		return nil, fmt.Errorf("manifestcodec.openBody: %w", err)
	}

	nonce := ciphertext[:aead.NonceLen]
	body := ciphertext[aead.NonceLen:]

	plaintext, err := gcm.Open(nil, nonce, body, aadBytes)
	if err != nil {
		// Fold all AEAD verification failures into the canonical
		// "decryption failed" sentinel.
		return nil, errs.ErrDecryptionFailed
	}
	return plaintext, nil
}
