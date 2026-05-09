package aesgcm

import (
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"

	"scrinium.dev/engine/core"
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

// factory is the AES-GCM TransformerFactory. It holds the AEAD
// primitive built from the DEK so that per-operation Encoders and
// Decoders do not pay the AES-key-schedule cost on construction.
type factory struct {
	aead cipher.AEAD
}

// New constructs an AES-256-GCM TransformerFactory bound to key.
// Returns nil and an error if key length is not 32 bytes.
func New(key []byte) (core.TransformerFactory, error) {
	if len(key) != keyBytes {
		return nil, fmt.Errorf("%w (got %d)", ErrInvalidKeyLength, len(key))
	}
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
	return &factory{aead: aead}, nil
}

// NewEncoder creates a fresh per-operation Encoder. The IV is
// generated lazily on the first Transform call.
func (f *factory) NewEncoder() core.Encoder {
	return &encoder{aead: f.aead}
}

// NewDecoder creates a fresh per-operation Decoder bound to the
// IV recorded in stage.IV at write time.
func (f *factory) NewDecoder(stage domain.PipelineStage) core.Decoder {
	return &decoder{aead: f.aead, iv: stage.IV}
}
