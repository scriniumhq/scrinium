package keyring

import (
	"crypto/rand"
	"fmt"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/store/internal/descriptor"
)

// DEKLen is the size of a Scrinium data-encryption key in bytes.
// Fixed at 32 — the AES-256-GCM key size that the wrap layer
// consumes.
const DEKLen = 32

// GenerateDEK returns a fresh DEK from crypto/rand. A failure here
// means the OS RNG is broken and is surfaced unchanged; retrying
// it makes no sense.
func GenerateDEK() ([]byte, error) {
	dek := make([]byte, DEKLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("keyring: generate DEK: %w", err)
	}
	return dek, nil
}

// WrapDEK turns a passphrase into a wrapped DEK ready for
// descriptor persistence. Generates a fresh salt, derives the KEK
// through Argon2id, and wraps dek with AES-256-GCM. The caller is
// responsible for zeroing the passphrase buffer after this call
// returns — WrapDEK does NOT mutate it.
//
// Returns the on-disk shape of the KDF parameters (algorithm,
// salt, cost) so the caller can fold them straight into a
// descriptor. The cost parameters come from cost; if cost is the
// zero value, DefaultKDFParams() is used.
func WrapDEK(dek, passphrase []byte, cost domain.KDFParams) ([]byte, descriptor.KDFParams, error) {
	if len(dek) != DEKLen {
		return nil, descriptor.KDFParams{}, fmt.Errorf("keyring: WrapDEK: dek length %d, want %d", len(dek), DEKLen)
	}
	if len(passphrase) == 0 {
		return nil, descriptor.KDFParams{}, errs.ErrPassphraseRequired
	}

	if cost == (domain.KDFParams{}) {
		cost = DefaultKDFParams()
	}
	if err := ValidateKDFParams(cost); err != nil {
		return nil, descriptor.KDFParams{}, err
	}

	salt, err := newSalt()
	if err != nil {
		return nil, descriptor.KDFParams{}, fmt.Errorf("keyring: WrapDEK: salt: %w", err)
	}

	kek := deriveKEK(passphrase, salt, cost.Time, cost.Memory, cost.Threads)
	defer aead.Wipe(kek)

	wrapped, err := wrapKEK(dek, kek)
	if err != nil {
		return nil, descriptor.KDFParams{}, fmt.Errorf("keyring: WrapDEK: %w", err)
	}

	return wrapped, descriptor.KDFParams{
		Algorithm: kdfAlgorithm,
		Time:      cost.Time,
		Memory:    cost.Memory,
		Threads:   cost.Threads,
		Salt:      salt,
	}, nil
}

// UnwrapDEK reverses WrapDEK against an on-disk KDFParams instance.
// Used by OpenStore auto-unlock, Store.Unlock, and RotateKEK (to
// extract the current DEK before re-wrapping).
//
// passphrase ownership: as in WrapDEK, the caller wipes its own
// buffer; UnwrapDEK does not.
//
// Errors:
//   - errs.ErrInvalidKDFParams — params fail ValidateKDFParams
//   - errs.ErrDecryptionFailed — wrong passphrase or tampered
//     wrappedDEK (folded together by the wrap layer on purpose)
func UnwrapDEK(wrappedDEK []byte, params descriptor.KDFParams, passphrase []byte) ([]byte, error) {
	if len(passphrase) == 0 {
		return nil, errs.ErrPassphraseRequired
	}
	cost := domain.KDFParams{Time: params.Time, Memory: params.Memory, Threads: params.Threads}
	if err := ValidateKDFParams(cost); err != nil {
		return nil, err
	}
	if params.Algorithm != kdfAlgorithm {
		return nil, fmt.Errorf("%w: algorithm %q (only %q supported)",
			errs.ErrInvalidKDFParams, params.Algorithm, kdfAlgorithm)
	}
	if len(params.Salt) != saltLen {
		return nil, fmt.Errorf("%w: salt length %d, want %d",
			errs.ErrInvalidKDFParams, len(params.Salt), saltLen)
	}

	kek := deriveKEK(passphrase, params.Salt, params.Time, params.Memory, params.Threads)
	defer aead.Wipe(kek)

	dek, err := unwrapKEK(wrappedDEK, kek)
	if err != nil {
		return nil, err // already wrapped with ErrDecryptionFailed
	}
	if len(dek) != DEKLen {
		// Defensive: a kit/descriptor that decrypts cleanly to the
		// wrong length means somebody encrypted nonsense with a
		// valid KEK. Treat as decryption failure to keep the error
		// surface small.
		return nil, fmt.Errorf("%w: unwrapped DEK length %d, want %d",
			errs.ErrDecryptionFailed, len(dek), DEKLen)
	}
	return dek, nil
}
