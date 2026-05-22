package keyring

import (
	"crypto/rand"
	"fmt"

	"golang.org/x/crypto/argon2"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
)

// kdfAlgorithm is the only KDF algorithm currently supported.
const kdfAlgorithm = "argon2id"

// saltLen is the length of the KDF salt in bytes. Fixed at 16 per
// RFC 9106 §4 recommendation; not configurable by callers.
const saltLen = 16

// kekLen is the length of the derived KEK in bytes. 32 — the key
// size of AES-256-GCM, the AEAD used to wrap the DEK.
const kekLen = 32

// Minimum cost bounds enforced by validateKDF. Mirror the comment
// on errs.ErrInvalidKDFParams.
const (
	minKDFTime    uint32 = 1
	minKDFMemory  uint32 = 19456 // KiB ≈ 19 MiB; OWASP 2024 floor
	minKDFThreads uint8  = 1
)

// DefaultKDFParams returns the cost parameters applied when
// InitStore is called without an explicit StoreConfig.KDFParams
// override.
//
// The returned struct is the client-facing shape — three cost
// fields, no salt, no algorithm. The salt is generated separately
// at wrap time via newSalt; the algorithm is fixed by this package.
func DefaultKDFParams() domain.KDFParams {
	return domain.KDFParams{
		Time:    1,
		Memory:  65536, // KiB = 64 MiB
		Threads: 4,
	}
}

// ValidateKDFParams runs the minimum-validity check on a client-
// supplied parameter set. Only the three cost fields are inspected;
// salt and algorithm are not part of the client surface.
//
// Returns a wrapped errs.ErrInvalidKDFParams with a concrete
// reason; errors.Is against ErrInvalidKDFParams matches every
// shape of failure.
func ValidateKDFParams(p domain.KDFParams) error {
	if p.Time < minKDFTime {
		return fmt.Errorf("%w: time=%d below minimum %d",
			errs.ErrInvalidKDFParams, p.Time, minKDFTime)
	}
	if p.Memory < minKDFMemory {
		return fmt.Errorf("%w: memory=%d KiB below minimum %d KiB",
			errs.ErrInvalidKDFParams, p.Memory, minKDFMemory)
	}
	if p.Threads < minKDFThreads {
		return fmt.Errorf("%w: threads=%d below minimum %d",
			errs.ErrInvalidKDFParams, p.Threads, minKDFThreads)
	}
	return nil
}

// newSalt returns saltLen bytes from crypto/rand. A failure here
// indicates a broken OS RNG and is not retried.
func newSalt() ([]byte, error) {
	b := make([]byte, saltLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// deriveKEK computes the KEK from a passphrase using Argon2id with
// the supplied parameters. The output is exactly kekLen (32) bytes.
//
// The passphrase byte slice is NOT zeroed by this function —
// callers wipe their own buffers after the derived KEK is no
// longer needed.
func deriveKEK(passphrase, salt []byte, time, memory uint32, threads uint8) []byte {
	return argon2.IDKey(passphrase, salt, time, memory, threads, kekLen)
}
