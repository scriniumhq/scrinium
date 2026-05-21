package aesgcm

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
)

// ErrInvalidKeyLength is returned by New when key is not 32 bytes.
// Wraps no sentinel — this is a programmer error at wiring time,
// not a runtime sentinel for clients to match on.
var ErrInvalidKeyLength = errors.New("aesgcm: key must be 32 bytes (AES-256)")

const (
	keyBytes = 32
	ivBytes  = 12
)

// factory is the pinned-DEK AES-GCM TransformerFactory. It holds
// the AEAD primitive built from a single key passed to New so that
// per-operation Decoders do not pay the AES key schedule cost on
// construction. It also retains the raw key: under EncryptedDedup
// Convergent the segmented encoder needs the DEK as the HMAC key for
// per-segment IV derivation (ADR-59). Use NewWithResolver for the
// multi-key path (rotation, multi-tenant, crypto-shredding).
type factory struct {
	key  []byte
	aead cipher.AEAD
}

// New constructs a pinned-DEK AES-256-GCM TransformerFactory bound
// to key. Returns nil and an error if key length is not 32 bytes.
//
// Pinned-DEK factories do NOT write a KeyID into pipeline stages
// and do NOT consult the StoreIndex's KeyResolver on read — they
// always use the key fixed at construction. This is the legacy
// single-key wiring; new code should prefer NewWithResolver.
func New(key []byte) (coreapi.TransformerFactory, error) {
	if len(key) != keyBytes {
		return nil, fmt.Errorf("%w (got %d)", ErrInvalidKeyLength, len(key))
	}
	aead, err := buildAEAD(key)
	if err != nil {
		return nil, err
	}
	// Copy the key so a caller mutating its slice cannot change our
	// IV derivation out from under in-flight writes.
	keyCopy := append([]byte(nil), key...)
	return &factory{key: keyCopy, aead: aead}, nil
}

// buildAEAD wraps the standard-library AES-GCM construction with
// the project's invariant checks. Shared by the pinned-DEK and
// resolver-DEK factories: both need exactly the same primitive
// per key, only the moment of construction differs.
func buildAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aesgcm: aes.NewCipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("aesgcm: cipher.NewGCM: %w", err)
	}
	if aead.NonceSize() != ivBytes {
		// Defensive: the standard library's GCM uses a 12-byte
		// nonce; document it as an invariant rather than a magic
		// constant.
		return nil, fmt.Errorf("aesgcm: unexpected nonce size %d", aead.NonceSize())
	}
	return aead, nil
}

// NewEncoder creates a fresh per-operation Encoder. The pinned-DEK
// path records an empty KeyID; it reads the segmentation mode and
// size from the EncodeContext the engine threads from StoreConfig
// (ADR-59).
func (f *factory) NewEncoder(ec coreapi.EncodeContext) coreapi.Encoder {
	return &encoder{
		aead:    f.aead,
		dek:     f.key,
		mode:    ivModeFor(ec.EncryptedDedup),
		segSize: ec.SegmentSize,
		// KeyID stays empty: pinned-DEK never records one.
	}
}

// NewDecoder creates a fresh per-operation Decoder. The IV is no
// longer taken from stage.IV — the segmented format stores one IV
// per segment frame inside the blob, so the decoder reads it from
// the stream (ADR-59).
func (f *factory) NewDecoder(_ domain.PipelineStage) coreapi.Decoder {
	return &decoder{aead: f.aead}
}

// AEAD implements coreapi.AEADCapable. The presence of this method
// lets the engine treat blobs encrypted by this plugin as
// AEAD-protected on the read path — each segment's GCM tag is
// verified by Decoder.Transform on every read, so an explicit
// ContentHash recomputation under VerifyOnRead=Auto would be
// redundant.
func (f *factory) AEAD() {}
