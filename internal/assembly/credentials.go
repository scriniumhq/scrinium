package assembly

import (
	"context"
	"fmt"
	decl "scrinium.dev/config/declarative"

	"scrinium.dev/domain"
)

func resolveCredentials(ctx context.Context, creds decl.Credentials) (map[string][]byte, error) {
	if len(creds) == 0 {
		return nil, nil
	}
	out := make(map[string][]byte, len(creds))
	for name, ref := range creds {
		b, err := ref.Resolve(ctx)
		if err != nil {
			return nil, fmt.Errorf("credential %q: %w", name, err)
		}
		out[name] = b
	}
	return out, nil
}

// passphraseProvider builds a domain.PassphraseProvider from the
// policy's encryption secret. The secret is resolved once at load
// time; the provider returns the same bytes on every prompt (init,
// unlock, rotation) — adequate for the file/env/plain schemes.
func passphraseProvider(ctx context.Context, p *decl.Policy) (domain.PassphraseProvider, error) {
	if p == nil || p.Encryption == nil {
		return nil, nil
	}
	secret, err := p.Encryption.Passphrase.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	return func(_ context.Context, _ domain.PassphraseHint) ([]byte, error) {
		// Hand back a copy: the engine zeroes the buffer after KEK
		// derivation, and we must survive a second prompt.
		cp := make([]byte, len(secret))
		copy(cp, secret)
		return cp, nil
	}, nil
}
