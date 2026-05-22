package keyring

import (
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/argon2"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/aead"
)

// kdfAlgorithm is the only KDF currently supported.
const kdfAlgorithm = "argon2id"

// saltLen is the KDF salt length (RFC 9106 §4); not configurable.
const saltLen = 16

// Minimum cost bounds enforced by ValidateKDFParams (OWASP 2024 floor).
const (
	minKDFTime    uint32 = 1
	minKDFMemory  uint32 = 19456 // KiB ≈ 19 MiB
	minKDFThreads uint8  = 1
)

// DefaultKDFParams returns the cost parameters used when InitStore is
// called without an explicit StoreConfig.KDFParams override. The
// salt is generated at wrap time; the algorithm is fixed by this
// package.
func DefaultKDFParams() domain.KDFParams {
	return domain.KDFParams{
		Time:    1,
		Memory:  65536, // KiB = 64 MiB
		Threads: 4,
	}
}

// ValidateKDFParams checks the three client-supplied cost fields.
// Failures wrap errs.ErrInvalidKDFParams.
func ValidateKDFParams(p domain.KDFParams) error {
	if p.Time < minKDFTime {
		return fmt.Errorf("%w: time=%d below minimum %d", errs.ErrInvalidKDFParams, p.Time, minKDFTime)
	}
	if p.Memory < minKDFMemory {
		return fmt.Errorf("%w: memory=%d KiB below minimum %d KiB", errs.ErrInvalidKDFParams, p.Memory, minKDFMemory)
	}
	if p.Threads < minKDFThreads {
		return fmt.Errorf("%w: threads=%d below minimum %d", errs.ErrInvalidKDFParams, p.Threads, minKDFThreads)
	}
	return nil
}

// newSalt returns saltLen random bytes. A failure means a broken OS
// RNG and is not retried.
func newSalt() ([]byte, error) {
	b := make([]byte, saltLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// deriveKEK computes the KEK from a passphrase via Argon2id. The
// output is exactly aead.DEKLen bytes (the AES-256 key size). The
// passphrase is not zeroed here — callers wipe their own buffers.
func deriveKEK(passphrase, salt []byte, time, memory uint32, threads uint8) []byte {
	return argon2.IDKey(passphrase, salt, time, memory, threads, aead.DEKLen)
}
