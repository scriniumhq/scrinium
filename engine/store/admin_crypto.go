package store

import "context"

// admin_crypto.go — the public AdminStore crypto methods. Each is a
// thin delegator to its multi-step implementation in
// admin_crypto_impl.go; keeping the surface here makes the AdminStore
// contract readable in one place.

// Unlock transitions an encrypted Store from Locked to Unlocked.
// Idempotent in Unlocked.
func (s *store) Unlock(ctx context.Context) error {
	return s.unlockEncrypted(ctx)
}

// ExportRecoveryKit returns the current Recovery Kit. Available in
// Unlocked and Degraded.
func (s *store) ExportRecoveryKit(ctx context.Context) ([]byte, error) {
	return s.exportRecoveryKitImpl(ctx)
}

// RotateKEK re-wraps the DEK under a new KEK. On-disk data is not
// rewritten; the prior Recovery Kit is invalidated.
func (s *store) RotateKEK(ctx context.Context) error {
	return s.rotateKEKImpl(ctx)
}

// SetPassphrase enables encryption on a Store initialised with a
// plaintext DEK. Refuses with errs.ErrPassphraseAlreadySet when the
// DEK is already wrapped — use RotateKEK then.
func (s *store) SetPassphrase(ctx context.Context) error {
	return s.setPassphraseImpl(ctx)
}
