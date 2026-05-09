package kdf

import (
	"crypto/rand"
	"fmt"

	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/errs"
)

// Algorithm is the only KDF algorithm currently supported.
const Algorithm = "argon2id"

// SaltLen is the length of the salt in bytes. Fixed at 16 per
// RFC 9106 §4 recommendation; not configurable by callers.
const SaltLen = 16

// KEKLen is the length of the derived key in bytes. 32 — the key
// size of AES-256-GCM, the AEAD used to wrap the DEK.
const KEKLen = 32

// Minimum bounds enforced by Validate. Mirror the comment on
// errs.ErrInvalidKDFParams.
const (
	MinTime    uint32 = 1
	MinMemory  uint32 = 19456 // KiB ≈ 19 MiB; OWASP 2024 floor
	MinThreads uint8  = 1
)

// Default returns the cost parameters applied when InitStore is
// called without an explicit StoreConfig.KDFParams override.
//
// The returned struct is the *client-facing* shape — three cost
// fields, no salt, no algorithm. The salt is generated separately
// at InitStore time via NewSalt; the algorithm is fixed by this
// package.
func Default() domain.KDFParams {
	return domain.KDFParams{
		Time:    1,
		Memory:  65536, // KiB = 64 MiB
		Threads: 4,
	}
}

// NewSalt returns SaltLen bytes from crypto/rand. A failure here
// indicates a broken OS RNG and is not retried.
func NewSalt() ([]byte, error) {
	b := make([]byte, SaltLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// Validate runs the minimum-validity check on a client-supplied
// parameter set. Only the three cost fields are inspected; salt
// and algorithm are not part of the client surface.
//
// Returns a wrapped errs.ErrInvalidKDFParams with a concrete
// reason; errors.Is against ErrInvalidKDFParams matches every
// shape of failure.
func Validate(p domain.KDFParams) error {
	if p.Time < MinTime {
		return fmt.Errorf("%w: time=%d below minimum %d",
			errs.ErrInvalidKDFParams, p.Time, MinTime)
	}
	if p.Memory < MinMemory {
		return fmt.Errorf("%w: memory=%d KiB below minimum %d KiB",
			errs.ErrInvalidKDFParams, p.Memory, MinMemory)
	}
	if p.Threads < MinThreads {
		return fmt.Errorf("%w: threads=%d below minimum %d",
			errs.ErrInvalidKDFParams, p.Threads, MinThreads)
	}
	return nil
}
