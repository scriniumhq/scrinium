package keyring

import (
	"crypto/rand"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/errs"
)

// GenerateDEK returns a fresh DEK from crypto/rand. A failure means a
// broken OS RNG and is surfaced unchanged.
func GenerateDEK() ([]byte, error) {
	dek := make([]byte, aead.DEKLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("keyring: generate DEK: %w", err)
	}
	return dek, nil
}

// WrapDEK derives a KEK from passphrase (Argon2id over a fresh salt)
// and wraps dek with AES-256-GCM, returning the wrapped bytes and the
// on-disk KDF parameters for the descriptor. A zero cost falls back
// to DefaultKDFParams. The caller owns and wipes passphrase; WrapDEK
// does not mutate it.
func WrapDEK(dek, passphrase []byte, cost domain.KDFParams) ([]byte, descriptor.KDFParams, error) {
	if len(dek) != aead.DEKLen {
		return nil, descriptor.KDFParams{}, fmt.Errorf("keyring: WrapDEK: dek length %d, want %d", len(dek), aead.DEKLen)
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

// UnwrapDEK reverses WrapDEK against on-disk KDFParams. Used by
// OpenStore auto-unlock, Store.Unlock, and RotateKEK. The caller owns
// and wipes passphrase.
//
// Errors: errs.ErrInvalidKDFParams (params fail validation);
// errs.ErrDecryptionFailed (wrong passphrase or tampered wrappedDEK —
// folded together on purpose).
func UnwrapDEK(wrappedDEK []byte, params descriptor.KDFParams, passphrase []byte) ([]byte, error) {
	if len(passphrase) == 0 {
		return nil, errs.ErrPassphraseRequired
	}
	cost := domain.KDFParams{Time: params.Time, Memory: params.Memory, Threads: params.Threads}
	if err := ValidateKDFParams(cost); err != nil {
		return nil, err
	}
	if params.Algorithm != kdfAlgorithm {
		return nil, fmt.Errorf("%w: algorithm %q (only %q supported)", errs.ErrInvalidKDFParams, params.Algorithm, kdfAlgorithm)
	}
	if len(params.Salt) != saltLen {
		return nil, fmt.Errorf("%w: salt length %d, want %d", errs.ErrInvalidKDFParams, len(params.Salt), saltLen)
	}

	kek := deriveKEK(passphrase, params.Salt, params.Time, params.Memory, params.Threads)
	defer aead.Wipe(kek)

	dek, err := unwrapKEK(wrappedDEK, kek)
	if err != nil {
		return nil, err
	}
	if len(dek) != aead.DEKLen {
		// Clean decrypt to the wrong length means valid-KEK over
		// nonsense; treat as decryption failure to keep the surface small.
		return nil, fmt.Errorf("%w: unwrapped DEK length %d, want %d", errs.ErrDecryptionFailed, len(dek), aead.DEKLen)
	}
	return dek, nil
}
