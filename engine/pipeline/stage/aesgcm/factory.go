package aesgcm

import (
	"crypto/cipher"
	"errors"
	"fmt"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/pipeline"
)

// ErrInvalidKeyLength is returned by New when key is not 32 bytes.
var ErrInvalidKeyLength = errors.New("aesgcm: key must be 32 bytes (AES-256)")

// factory is the pinned-DEK AES-GCM TransformerFactory. It holds the
// AEAD primitive built from a single key passed to New, and retains
// the raw key: under EncryptedDedup Convergent the segmented encoder
// needs the DEK as the HMAC key for per-segment IV derivation
// (ADR-59). Use NewWithResolver for the multi-key path.
type factory struct {
	key []byte
	gcm cipher.AEAD
}

// New constructs a pinned-DEK AES-256-GCM TransformerFactory bound to
// key. Returns nil and an error if key length is not 32 bytes.
//
// Pinned-DEK factories do NOT write a KeyID into pipeline stages and
// do NOT consult the KeyResolver on read. New code should prefer
// NewWithResolver.
func New(key []byte) (pipeline.TransformerFactory, error) {
	if len(key) != aead.DEKLen {
		return nil, fmt.Errorf("%w (got %d)", ErrInvalidKeyLength, len(key))
	}
	gcm, err := buildAEAD(key)
	if err != nil {
		return nil, err
	}
	keyCopy := append([]byte(nil), key...)
	return &factory{key: keyCopy, gcm: gcm}, nil
}

// buildAEAD constructs the AES-256-GCM primitive via the shared
// internal/aead constructor (the one home for the 32-byte-key /
// 12-byte-nonce invariants). Shared by the pinned-DEK and resolver
// factories.
func buildAEAD(key []byte) (cipher.AEAD, error) {
	return aead.NewGCM(key)
}

// NewEncoder creates a fresh per-operation Encoder. The pinned-DEK
// path records an empty KeyID; it reads the segmentation mode and
// size from EncodeContext (ADR-59).
func (f *factory) NewEncoder(ec pipeline.EncodeContext) pipeline.Encoder {
	return &encoder{
		gcm:     f.gcm,
		dek:     f.key,
		mode:    ivModeFor(ec.EncryptedDedup),
		segSize: ec.SegmentSize,
	}
}

// NewDecoder creates a fresh per-operation Decoder. The IV comes from
// each segment frame, not stage.IV (ADR-59).
func (f *factory) NewDecoder(_ domain.PipelineStage) pipeline.Decoder {
	return &decoder{gcm: f.gcm}
}

// AEAD implements pipeline.AEADCapable.
func (f *factory) AEAD() {}
