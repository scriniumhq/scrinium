package core

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/rkurbatov/scrinium/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/core/internal/kdf"
	"github.com/rkurbatov/scrinium/core/internal/keywrap"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
)

// dekLen is the size of a Scrinium data-encryption key in bytes.
// Fixed at 32 — the AES-256-GCM key size that keywrap consumes.
const dekLen = 32

// generateDEK returns a fresh DEK from crypto/rand. A failure
// here means the OS RNG is broken and is surfaced unchanged;
// retrying it makes no sense.
func generateDEK() ([]byte, error) {
	dek := make([]byte, dekLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("core: generate DEK: %w", err)
	}
	return dek, nil
}

// wrapDEK turns a passphrase into a wrapped DEK ready for
// descriptor persistence. Generates a fresh salt, derives the
// KEK through Argon2id, and wraps dek with AES-256-GCM. The
// caller is responsible for zeroing the passphrase buffer
// after this call returns — wrapDEK does NOT mutate it, so the
// caller keeps full control.
//
// Returns the on-disk shape of the KDF parameters (algorithm,
// salt, cost) so the caller can fold them straight into a
// descriptor. The cost parameters come from cost; if cost is
// the zero value, kdf.Default() is used.
func wrapDEK(dek, passphrase []byte, cost domain.KDFParams) ([]byte, descriptor.KDFParams, error) {
	if len(dek) != dekLen {
		return nil, descriptor.KDFParams{}, fmt.Errorf("core: wrapDEK: dek length %d, want %d", len(dek), dekLen)
	}
	if len(passphrase) == 0 {
		return nil, descriptor.KDFParams{}, errs.ErrPassphraseRequired
	}

	if cost == (domain.KDFParams{}) {
		cost = kdf.Default()
	}
	if err := kdf.Validate(cost); err != nil {
		return nil, descriptor.KDFParams{}, err
	}

	salt, err := kdf.NewSalt()
	if err != nil {
		return nil, descriptor.KDFParams{}, fmt.Errorf("core: wrapDEK: salt: %w", err)
	}

	kek := kdf.Derive(passphrase, salt, cost.Time, cost.Memory, cost.Threads)
	defer wipeSecret(kek)

	wrapped, err := keywrap.Wrap(dek, kek)
	if err != nil {
		return nil, descriptor.KDFParams{}, fmt.Errorf("core: wrapDEK: %w", err)
	}

	return wrapped, descriptor.KDFParams{
		Algorithm: kdf.Algorithm,
		Time:      cost.Time,
		Memory:    cost.Memory,
		Threads:   cost.Threads,
		Salt:      salt,
	}, nil
}

// unwrapDEK reverses wrapDEK against an on-disk KDFParams
// instance. Used by OpenStore auto-unlock, Store.Unlock, and
// RotateKEK (to extract the current DEK before re-wrapping).
//
// passphrase ownership: as in wrapDEK, the caller wipes its own
// buffer; unwrapDEK does not.
//
// Errors:
//   - errs.ErrInvalidKDFParams — params fail kdf.Validate
//   - errs.ErrDecryptionFailed — wrong passphrase or tampered
//     wrappedDEK (folded together by
//     keywrap on purpose)
func unwrapDEK(wrappedDEK []byte, params descriptor.KDFParams, passphrase []byte) ([]byte, error) {
	if len(passphrase) == 0 {
		return nil, errs.ErrPassphraseRequired
	}
	cost := domain.KDFParams{Time: params.Time, Memory: params.Memory, Threads: params.Threads}
	if err := kdf.Validate(cost); err != nil {
		return nil, err
	}
	if params.Algorithm != kdf.Algorithm {
		return nil, fmt.Errorf("%w: algorithm %q (only %q supported)",
			errs.ErrInvalidKDFParams, params.Algorithm, kdf.Algorithm)
	}
	if len(params.Salt) != kdf.SaltLen {
		return nil, fmt.Errorf("%w: salt length %d, want %d",
			errs.ErrInvalidKDFParams, len(params.Salt), kdf.SaltLen)
	}

	kek := kdf.Derive(passphrase, params.Salt, params.Time, params.Memory, params.Threads)
	defer wipeSecret(kek)

	dek, err := keywrap.Unwrap(wrappedDEK, kek)
	if err != nil {
		return nil, err // already wrapped with ErrDecryptionFailed
	}
	if len(dek) != dekLen {
		// Defensive: a kit/descriptor that decrypts cleanly to
		// the wrong length means somebody encrypted nonsense
		// with a valid KEK. Treat as decryption failure to keep
		// the error surface small.
		return nil, fmt.Errorf("%w: unwrapped DEK length %d, want %d",
			errs.ErrDecryptionFailed, len(dek), dekLen)
	}
	return dek, nil
}

// callProvider invokes the configured PassphraseProvider with
// the given hint, classifying its error returns. A nil provider
// surfaces ErrPassphraseRequired; a provider that returns an
// error gets that error wrapped with ErrPassphraseProvider so
// callers can branch with errors.Is.
//
// The returned slice is owned by the caller and MUST be wiped
// with wipeSecret once the KEK has been derived. callProvider
// does not retain a reference.
func callProvider(ctx context.Context, p PassphraseProvider, hint PassphraseHint) ([]byte, error) {
	if p == nil {
		return nil, errs.ErrPassphraseRequired
	}
	pass, err := p(ctx, hint)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errs.ErrPassphraseProvider, err)
	}
	if len(pass) == 0 {
		return nil, errs.ErrPassphraseRequired
	}
	return pass, nil
}

// wipeSecret overwrites b with zeros. Used for KEK and passphrase
// buffers as soon as they are no longer needed. Defends against
// the trivial leakage path: a goroutine that holds a reference
// to a slice of memory after the engine is "done" with it.
//
// Note: Go's runtime can copy or relocate slice contents
// (escape analysis, GC compaction in future runtimes). wipeSecret
// is a best-effort hygiene measure, not a security guarantee.
// For threat models that require it (HSM-grade), use
// memguard-style locked memory in the host application.
//
// The wrapper around the clear() builtin is kept deliberately:
// the name documents intent at every call site (we are zeroing
// a secret, not initialising a slice).
func wipeSecret(b []byte) {
	clear(b)
}

// promoteKeyResolverIfDefault installs a default StaticKeyResolver
// over s.dek, ONLY when the caller did not pass their own
// WithKeyResolver. Idempotent: a second call (after re-Unlock,
// say) overwrites the resolver only if it's still nil.
//
// The discipline is "do not surprise the caller": if they took
// the trouble to construct a custom resolver (multi-tenant,
// HSM-backed, etc.), the engine respects it and does not overwrite.
//
// Caller must hold s.cryptoMu.
func (s *store) promoteKeyResolverIfDefault() {
	if s.keyResolver != nil {
		return
	}
	if len(s.dek) == 0 {
		return
	}
	s.keyResolver = NewStaticKeyResolver(s.dek)
}
