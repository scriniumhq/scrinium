package store

import (
	"bytes"
	"context"
	"fmt"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/keyring"
)

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

// Shared discipline for the crypto methods below:
//   - crypto.mu guards s.crypto.desc / .dek / .provider for the whole
//     of each operation; none is on a hot path, so serialising is fine.
//   - stateMu is taken only briefly, for the state transition.
//   - Every passphrase from callProvider is wiped immediately after the
//     KEK is derived; KEKs are wiped inside the keyring helpers.
//
// Concurrent writers from another process sharing the Location are out
// of scope: these methods do not re-read the descriptor before mutating
// it, so single-process usage is the only supported configuration.

// unlockEncrypted is the body of Store.Unlock for an encrypted
// Store currently in StateLocked. It invokes the configured
// PassphraseProvider with Reason="unlock", unwraps the DEK,
// runs the deferred Orphan Scan, and transitions the Store to
// StateUnlocked.
//
// In any other state the method short-circuits as a successful
// no-op (idempotent), matching the contract of the public
// Unlock. State checks happen BEFORE provider invocation, so an
// already-Unlocked Store does not prompt the user.
func (s *store) unlockEncrypted(ctx context.Context) error {
	// enterAdmin applies the closed/corrupted/offline/bootstrapping
	// gate but allows Locked through — the whole point of Unlock is
	// to leave Locked. Specific Unlocked-vs-Locked logic stays in
	// the state switch below.
	if err := s.enterAdmin(ctx); err != nil {
		return err
	}
	s.crypto.mu.Lock()
	defer s.crypto.mu.Unlock()

	// Idempotent fast path. Holds cryptoMu so a concurrent
	// SetPassphrase/RotateKEK cannot race with the State read.
	switch s.State() {
	case domain.StateUnlocked:
		return nil
	case domain.StateLocked:
		// Proceed below.
	default:
		return fmt.Errorf("store.Unlock: state %v rejects unlock", s.State())
	}

	// Plain DEK in Locked state would be a bug — Plain Stores
	// open to Unlocked unconditionally. Defensive surface; if
	// we hit it, the open-path invariant is broken.
	if s.crypto.desc == nil || !s.crypto.desc.DEKEncrypted || s.crypto.desc.KDFParams == nil {
		return fmt.Errorf("%w: descriptor in Locked state lacks crypto fields",
			errs.ErrStoreCorrupted)
	}

	provider := s.crypto.provider
	passphrase, err := callProvider(ctx, provider, PassphraseHint{
		StoreID: s.storeID,
		Reason:  "unlock",
	})
	if err != nil {
		return fmt.Errorf("store.Unlock: %w", err)
	}

	dek, err := keyring.UnwrapDEK(s.crypto.desc.DEK, *s.crypto.desc.KDFParams, passphrase)
	aead.Wipe(passphrase)
	if err != nil {
		return fmt.Errorf("store.Unlock: %w", err)
	}

	s.crypto.dek = dek
	s.crypto.promoteResolverIfDefault()

	// Bootstrap-into-Unlocked: same path as the AutoUnlock leg
	// of OpenStore. State first goes to Bootstrapping inside
	// unlockBootstrap (via the Orphan Scan run), then to
	// Unlocked. Failure leaves the Store in Bootstrapping; the
	// caller can retry by opening fresh.
	s.stateMu.Lock()
	s.state = domain.StateBootstrapping
	s.stateMu.Unlock()

	if err := unlockBootstrap(ctx, s, s.pub); err != nil {
		// Wipe the DEK we just unwrapped — the Store is not
		// safely operational, holding the key in memory adds
		// risk without benefit.
		aead.Wipe(s.crypto.dek)
		s.crypto.dek = nil
		s.stateMu.Lock()
		s.state = domain.StateLocked
		s.stateMu.Unlock()
		return fmt.Errorf("store.Unlock: %w", err)
	}
	return nil
}

// setPassphraseImpl is the body of Store.SetPassphrase. Wraps
// the currently-plaintext DEK with a KEK derived from the
// supplied passphrase, persists the new descriptor (Sequence+1),
// and refreshes the L2 cache.
//
// Refusal cases:
//   - state != Unlocked          → state error
//   - DEKEncrypted already true  → ErrPassphraseAlreadySet
//   - provider not configured    → ErrPassphraseRequired
//   - maintenance != None        → ErrStoreReadOnly / ErrStoreOffline
func (s *store) setPassphraseImpl(ctx context.Context) error {
	if err := s.enterWrite(ctx); err != nil {
		return err
	}
	s.crypto.mu.Lock()
	defer s.crypto.mu.Unlock()
	// SetPassphrase rewrites the descriptor (Sequence+1) into both
	// replicas. In Degraded the replicas are already out of sync —
	// piling another write on top is unsafe until Auto-Heal reaches
	// Unlocked. Refuse explicitly; checkWritable does not catch this
	// because Degraded is "API available, but consensus pending".
	if state := s.State(); state == domain.StateDegraded {
		return fmt.Errorf("store.SetPassphrase: state %v rejects write; wait for Auto-Heal", state)
	}
	if s.crypto.desc == nil {
		return fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if s.crypto.desc.DEKEncrypted {
		return errs.ErrPassphraseAlreadySet
	}
	if len(s.crypto.dek) == 0 {
		// A Plain Store must have a plaintext DEK after InitStore; if it
		// doesn't, the open-path invariant is broken.
		return fmt.Errorf("%w: Plain Store has no plaintext DEK", errs.ErrStoreCorrupted)
	}

	passphrase, err := callProvider(ctx, s.crypto.provider, PassphraseHint{
		StoreID: s.storeID,
		Reason:  "set_passphrase",
	})
	if err != nil {
		return fmt.Errorf("store.SetPassphrase: %w", err)
	}

	// Cost: take from active config if the caller specified
	// KDFParams there; otherwise default. The descriptor's
	// KDFParams is currently nil (Plain Store), so it cannot
	// be the source.
	var cost domain.KDFParams
	if cfg := s.snapshotConfig(); cfg.KDFParams != nil {
		cost = *cfg.KDFParams
	}

	wrapped, kdfParams, err := keyring.WrapDEK(s.crypto.dek, passphrase, cost)
	aead.Wipe(passphrase)
	if err != nil {
		return fmt.Errorf("store.SetPassphrase: %w", err)
	}

	next := *s.crypto.desc // shallow copy is fine; we'll replace pointer fields
	next.Sequence = s.crypto.desc.Sequence + 1
	next.DEK = wrapped
	next.DEKEncrypted = true
	next.KDFParams = &kdfParams

	if err := s.commitDescriptor(ctx, &next); err != nil {
		return fmt.Errorf("store.SetPassphrase: %w", err)
	}
	return nil
}

// rotateKEKImpl is the body of Store.RotateKEK. Re-wraps the
// existing DEK under a new KEK derived from a new passphrase,
// proven by first re-deriving and verifying the current one.
//
// The current-passphrase verification is deliberate even though
// the Store is Unlocked and we already hold the DEK in memory.
// It is a safety check against "left the laptop unlocked" —
// rotation is a change of the access boundary, and it must
// require possession of the current credential.
//
// Refusal cases:
//   - state != Unlocked                  → state error
//   - DEKEncrypted false                 → use SetPassphrase first
//   - current-passphrase verification fails → ErrDecryptionFailed
//   - provider not configured            → ErrPassphraseRequired
//   - maintenance != None                → ErrStoreReadOnly / ErrStoreOffline
func (s *store) rotateKEKImpl(ctx context.Context) error {
	if err := s.enterWrite(ctx); err != nil {
		return err
	}
	s.crypto.mu.Lock()
	defer s.crypto.mu.Unlock()
	// RotateKEK rewrites the descriptor (Sequence+1) into both
	// replicas. In Degraded the replicas are already out of sync —
	// piling another write on top is unsafe until Auto-Heal reaches
	// Unlocked. Refuse explicitly; checkWritable does not catch this
	// because Degraded is "API available, but consensus pending".
	if state := s.State(); state == domain.StateDegraded {
		return fmt.Errorf("store.RotateKEK: state %v rejects write; wait for Auto-Heal", state)
	}
	if s.crypto.desc == nil {
		return fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if !s.crypto.desc.DEKEncrypted {
		return fmt.Errorf("%w: Store has plaintext DEK; use SetPassphrase",
			errs.ErrPassphraseRequired)
	}
	if s.crypto.desc.KDFParams == nil {
		return fmt.Errorf("%w: encrypted descriptor lacks KDFParams",
			errs.ErrStoreCorrupted)
	}

	// First half: prove possession of the current passphrase
	// by unwrapping the DEK and matching it against s.crypto.dek. The
	// "unlock" reason mirrors Store.Unlock — host implementations
	// that retrieve passphrases from a keychain key off Reason and
	// expect the same lookup as a regular unlock.
	currentPass, err := callProvider(ctx, s.crypto.provider, PassphraseHint{
		StoreID: s.storeID,
		Reason:  "unlock",
	})
	if err != nil {
		return fmt.Errorf("store.RotateKEK: current passphrase: %w", err)
	}
	verified, err := keyring.UnwrapDEK(s.crypto.desc.DEK, *s.crypto.desc.KDFParams, currentPass)
	aead.Wipe(currentPass)
	if err != nil {
		return fmt.Errorf("store.RotateKEK: %w", err)
	}
	if !bytes.Equal(verified, s.crypto.dek) {
		aead.Wipe(verified)
		// Should never happen: if the passphrase unwrapped, it
		// must have produced the same DEK that's already in
		// memory. Surface as corruption since the alternative
		// is a defective KDF/AEAD round-trip.
		return fmt.Errorf("%w: current-passphrase unwrap produced unexpected DEK",
			errs.ErrStoreCorrupted)
	}
	aead.Wipe(verified)

	// Second half: obtain new passphrase, wrap with the same
	// cost parameters as before (rotation does not retune
	// cost; that would be a separate operation).
	newPass, err := callProvider(ctx, s.crypto.provider, PassphraseHint{
		StoreID: s.storeID,
		Reason:  "kek_rotation",
	})

	if err != nil {
		// callProvider returns nil on error, so without this guard the
		// WrapDEK below sees an empty passphrase and returns
		// ErrPassphraseRequired, masking the provider's real failure
		// and pointing the operator at the wrong cause. No descriptor
		// is written (WrapDEK fails before commitDescriptor), so this
		// is an error-contract/diagnostics bug, not a lockout. Mirror
		// the first-half "current passphrase" check.
		return fmt.Errorf("store.RotateKEK: new passphrase: %w", err)
	}

	cost := domain.KDFParams{
		Time:    s.crypto.desc.KDFParams.Time,
		Memory:  s.crypto.desc.KDFParams.Memory,
		Threads: s.crypto.desc.KDFParams.Threads,
	}
	wrapped, kdfParams, err := keyring.WrapDEK(s.crypto.dek, newPass, cost)
	aead.Wipe(newPass)
	if err != nil {
		return fmt.Errorf("store.RotateKEK: %w", err)
	}

	next := *s.crypto.desc
	next.Sequence = s.crypto.desc.Sequence + 1
	next.DEK = wrapped
	next.KDFParams = &kdfParams
	// DEKEncrypted stays true; DEK bytes are only the wrapping change.

	if err := s.commitDescriptor(ctx, &next); err != nil {
		return fmt.Errorf("store.RotateKEK: %w", err)
	}

	// Note: s.crypto.dek does NOT change. The DEK is re-wrapped under
	// a new KEK; the data-encryption key itself is the same,
	// which is precisely the point — RotateKEK costs O(1),
	// not O(data).
	return nil
}

// exportRecoveryKitImpl is the body of Store.ExportRecoveryKit.
// Stateless: assembles the kit from the current descriptor and
// in-memory DEK using the same buildRecoveryKit helper as
// InitStore.
//
// Refusal cases:
//   - state not in {Unlocked, Degraded} → state error
//   - DEKEncrypted false                → ErrPassphraseRequired
func (s *store) exportRecoveryKitImpl(ctx context.Context) ([]byte, error) {
	if err := s.enterRead(ctx); err != nil {
		return nil, err
	}
	s.crypto.mu.Lock()
	defer s.crypto.mu.Unlock()

	switch s.State() {
	case domain.StateUnlocked, domain.StateDegraded:
		// OK
	default:
		return nil, fmt.Errorf("store.ExportRecoveryKit: state %v rejects export",
			s.State())
	}

	if s.crypto.desc == nil {
		return nil, fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if !s.crypto.desc.DEKEncrypted {
		return nil, fmt.Errorf("%w: Plain Store has no Recovery Kit",
			errs.ErrPassphraseRequired)
	}

	return buildRecoveryKit(s.crypto.desc, s.crypto.desc.DEK)
}

// commitDescriptor is the shared tail of SetPassphrase and
// RotateKEK: persist the next descriptor (both replicas), refresh
// the L2 cache, and atomically swap s.crypto.desc.
//
// Caller must hold s.crypto.mu. On any error the caller's in-
// memory state (s.crypto.desc, s.crypto.dek) is left unchanged so a retry
// re-derives from the same starting point.
func (s *store) commitDescriptor(ctx context.Context, next *descriptor.Descriptor) error {
	if err := descriptor.Persist(ctx, s.drv, next); err != nil {
		return fmt.Errorf("persist descriptor: %w", err)
	}
	if err := descriptor.Save(ctx, s.index, next); err != nil {
		// Persisted on disk but cache write failed. The next
		// OpenStore will rebuild the cache from Location, so
		// this is recoverable; surface the error so the caller
		// knows the operation was not fully successful.
		return fmt.Errorf("save L2 cache: %w", err)
	}
	s.crypto.desc = next
	return nil
}

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
