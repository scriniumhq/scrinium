package keyring

import (
	"crypto/rand"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// saltLen is the KDF salt length (RFC 9106 §4); not configurable.
const saltLen = 16

// Minimum cost bounds enforced by ValidateKDFParams (OWASP 2024 floor).
const (
	minKDFTime    uint32 = 1
	minKDFMemory  uint32 = 19456 // KiB ≈ 19 MiB
	minKDFThreads uint8  = 1
)

// DefaultKDFParams returns the cost parameters used when InitStore is
// called without an explicit StoreConfig.KDFParams override. The salt
// is generated at wrap time; the algorithm is fixed by this package.
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
