package store

import (
	"context"
	"fmt"

	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/pipeline"
)

// crypto.go — store-side crypto glue that cannot live in the
// keyring package: the passphrase-provider call (bound to the
// store-public PassphraseProvider type) and the default
// key-resolver promotion (a *store method). The DEK/KEK primitives
// (GenerateDEK/WrapDEK/UnwrapDEK and the KDF + key-wrap kernels)
// live in store/internal/keyring.

// callProvider invokes the configured PassphraseProvider with the
// given hint, classifying its error returns. A nil provider
// surfaces ErrPassphraseRequired; a provider that returns an error
// gets that error wrapped with ErrPassphraseProvider so callers
// can branch with errors.Is.
//
// The returned slice is owned by the caller and MUST be wiped with
// aead.Wipe once the KEK has been derived. callProvider
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

// promoteKeyResolverIfDefault installs a default StaticKeyResolver
// over s.dek, ONLY when the caller did not pass their own
// WithKeyResolver. Idempotent: a second call (after re-Unlock, say)
// overwrites the resolver only if it's still nil.
//
// The discipline is "do not surprise the caller": if they took the
// trouble to construct a custom resolver (multi-tenant, HSM-backed,
// etc.), the engine respects it and does not overwrite.
//
// Caller must hold s.cryptoMu.
func (s *store) promoteKeyResolverIfDefault() {
	if s.keyResolver != nil {
		return
	}
	if len(s.dek) == 0 {
		return
	}
	s.keyResolver = pipeline.NewStaticKeyResolver(s.dek)
}
