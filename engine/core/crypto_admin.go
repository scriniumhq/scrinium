package core

import (
	"bytes"
	"context"
	"fmt"

	"scrinium.dev/engine/core/internal/descriptor"
	"scrinium.dev/engine/core/internal/descriptorcache"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/manifestcrypto"
)

// AdminStore crypto methods. The implementations live here
// rather than in store_impl.go to keep crypto state mutations
// (descriptor + dek + sequence bump + Persist + cache) in one
// readable file.
//
// Concurrency:
//   - cryptoMu guards reads and writes of s.desc, s.dek,
//     s.passphraseProvider for the duration of every operation
//     here. None of these methods is on a hot path; serialising
//     them is fine.
//   - stateMu is taken briefly when transitioning state.
//
// Multi-process caveat: between OpenStore and these calls,
// another process holding the same Location can have rewritten
// the descriptor. M2.2 has no lease (lands M3.1); concurrent
// writers are out of scope. This implementation does NOT
// re-read the descriptor from disk before mutation; one-process
// usage is the only supported configuration.
//
// Passphrase hygiene: every passphrase byte slice obtained from
// callProvider is wiped via manifestcrypto.Wipe immediately after the KEK
// has been derived. KEKs themselves are wiped inside wrapDEK and
// unwrapDEK helpers; this file does not handle them directly.

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
	s.cryptoMu.Lock()
	defer s.cryptoMu.Unlock()

	// Idempotent fast path. Holds cryptoMu so a concurrent
	// SetPassphrase/RotateKEK cannot race with the State read.
	switch s.State() {
	case domain.StateUnlocked:
		return nil
	case domain.StateLocked:
		// Proceed below.
	default:
		return fmt.Errorf("core.Unlock: state %v rejects unlock", s.State())
	}

	// Plain DEK in Locked state would be a bug — Plain Stores
	// open to Unlocked unconditionally. Defensive surface; if
	// we hit it, the open-path invariant is broken.
	if s.desc == nil || !s.desc.DEKEncrypted || s.desc.KDFParams == nil {
		return fmt.Errorf("%w: descriptor in Locked state lacks crypto fields",
			errs.ErrStoreCorrupted)
	}

	provider := s.passphraseProvider
	passphrase, err := callProvider(ctx, provider, PassphraseHint{
		StoreID: s.storeID,
		Reason:  "unlock",
	})
	if err != nil {
		return fmt.Errorf("core.Unlock: %w", err)
	}

	dek, err := unwrapDEK(s.desc.DEK, *s.desc.KDFParams, passphrase)
	manifestcrypto.Wipe(passphrase)
	if err != nil {
		return fmt.Errorf("core.Unlock: %w", err)
	}

	s.dek = dek
	s.promoteKeyResolverIfDefault()

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
		manifestcrypto.Wipe(s.dek)
		s.dek = nil
		s.stateMu.Lock()
		s.state = domain.StateLocked
		s.stateMu.Unlock()
		return fmt.Errorf("core.Unlock: %w", err)
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
	s.cryptoMu.Lock()
	defer s.cryptoMu.Unlock()
	// SetPassphrase rewrites the descriptor (Sequence+1) into both
	// replicas. In Degraded the replicas are already out of sync —
	// piling another write on top is unsafe until Auto-Heal reaches
	// Unlocked. Refuse explicitly; checkWritable does not catch this
	// because Degraded is "API available, but consensus pending".
	if state := s.State(); state == domain.StateDegraded {
		return fmt.Errorf("core.SetPassphrase: state %v rejects write; wait for Auto-Heal", state)
	}
	if s.desc == nil {
		return fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if s.desc.DEKEncrypted {
		return errs.ErrPassphraseAlreadySet
	}
	if len(s.dek) == 0 {
		// Plain Store must have a plaintext DEK after InitStore
		// per §3.1. If it doesn't, the open-path invariant is
		// broken.
		return fmt.Errorf("%w: Plain Store has no plaintext DEK", errs.ErrStoreCorrupted)
	}

	passphrase, err := callProvider(ctx, s.passphraseProvider, PassphraseHint{
		StoreID: s.storeID,
		Reason:  "set_passphrase",
	})
	if err != nil {
		return fmt.Errorf("core.SetPassphrase: %w", err)
	}

	// Cost: take from active config if the caller specified
	// KDFParams there; otherwise default. The descriptor's
	// KDFParams is currently nil (Plain Store), so it cannot
	// be the source.
	var cost domain.KDFParams
	if cfg := s.snapshotConfig(); cfg.KDFParams != nil {
		cost = *cfg.KDFParams
	}

	wrapped, kdfParams, err := wrapDEK(s.dek, passphrase, cost)
	manifestcrypto.Wipe(passphrase)
	if err != nil {
		return fmt.Errorf("core.SetPassphrase: %w", err)
	}

	next := *s.desc // shallow copy is fine; we'll replace pointer fields
	next.Sequence = s.desc.Sequence + 1
	next.DEK = wrapped
	next.DEKEncrypted = true
	next.KDFParams = &kdfParams

	if err := s.commitDescriptor(ctx, &next); err != nil {
		return fmt.Errorf("core.SetPassphrase: %w", err)
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
	s.cryptoMu.Lock()
	defer s.cryptoMu.Unlock()
	// RotateKEK rewrites the descriptor (Sequence+1) into both
	// replicas. In Degraded the replicas are already out of sync —
	// piling another write on top is unsafe until Auto-Heal reaches
	// Unlocked. Refuse explicitly; checkWritable does not catch this
	// because Degraded is "API available, but consensus pending".
	if state := s.State(); state == domain.StateDegraded {
		return fmt.Errorf("core.RotateKEK: state %v rejects write; wait for Auto-Heal", state)
	}
	if s.desc == nil {
		return fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if !s.desc.DEKEncrypted {
		return fmt.Errorf("%w: Store has plaintext DEK; use SetPassphrase",
			errs.ErrPassphraseRequired)
	}
	if s.desc.KDFParams == nil {
		return fmt.Errorf("%w: encrypted descriptor lacks KDFParams",
			errs.ErrStoreCorrupted)
	}

	// First half: prove possession of the current passphrase
	// by unwrapping the DEK and matching it against s.dek. The
	// "unlock" reason mirrors Store.Unlock — host implementations
	// that retrieve passphrases from a keychain key off Reason and
	// expect the same lookup as a regular unlock.
	currentPass, err := callProvider(ctx, s.passphraseProvider, PassphraseHint{
		StoreID: s.storeID,
		Reason:  "unlock",
	})
	if err != nil {
		return fmt.Errorf("core.RotateKEK: current passphrase: %w", err)
	}
	verified, err := unwrapDEK(s.desc.DEK, *s.desc.KDFParams, currentPass)
	manifestcrypto.Wipe(currentPass)
	if err != nil {
		return fmt.Errorf("core.RotateKEK: %w", err)
	}
	if !bytes.Equal(verified, s.dek) {
		manifestcrypto.Wipe(verified)
		// Should never happen: if the passphrase unwrapped, it
		// must have produced the same DEK that's already in
		// memory. Surface as corruption since the alternative
		// is a defective KDF/AEAD round-trip.
		return fmt.Errorf("%w: current-passphrase unwrap produced unexpected DEK",
			errs.ErrStoreCorrupted)
	}
	manifestcrypto.Wipe(verified)

	// Second half: obtain new passphrase, wrap with the same
	// cost parameters as before (rotation does not retune
	// cost; that would be a separate operation).
	newPass, err := callProvider(ctx, s.passphraseProvider, PassphraseHint{
		StoreID: s.storeID,
		Reason:  "kek_rotation",
	})

	cost := domain.KDFParams{
		Time:    s.desc.KDFParams.Time,
		Memory:  s.desc.KDFParams.Memory,
		Threads: s.desc.KDFParams.Threads,
	}
	wrapped, kdfParams, err := wrapDEK(s.dek, newPass, cost)
	manifestcrypto.Wipe(newPass)
	if err != nil {
		return fmt.Errorf("core.RotateKEK: %w", err)
	}

	next := *s.desc
	next.Sequence = s.desc.Sequence + 1
	next.DEK = wrapped
	next.KDFParams = &kdfParams
	// DEKEncrypted stays true; DEK bytes are only the wrapping change.

	if err := s.commitDescriptor(ctx, &next); err != nil {
		return fmt.Errorf("core.RotateKEK: %w", err)
	}

	// Note: s.dek does NOT change. The DEK is re-wrapped under
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
	s.cryptoMu.Lock()
	defer s.cryptoMu.Unlock()

	switch s.State() {
	case domain.StateUnlocked, domain.StateDegraded:
		// OK
	default:
		return nil, fmt.Errorf("core.ExportRecoveryKit: state %v rejects export",
			s.State())
	}

	if s.desc == nil {
		return nil, fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if !s.desc.DEKEncrypted {
		return nil, fmt.Errorf("%w: Plain Store has no Recovery Kit",
			errs.ErrPassphraseRequired)
	}

	return buildRecoveryKit(s.desc, s.desc.DEK)
}

// commitDescriptor is the shared tail of SetPassphrase and
// RotateKEK: persist the next descriptor (both replicas), refresh
// the L2 cache, and atomically swap s.desc.
//
// Caller must hold s.cryptoMu. On any error the caller's in-
// memory state (s.desc, s.dek) is left unchanged so a retry
// re-derives from the same starting point.
func (s *store) commitDescriptor(ctx context.Context, next *descriptor.Descriptor) error {
	if err := descriptor.Persist(ctx, s.drv, next); err != nil {
		return fmt.Errorf("persist descriptor: %w", err)
	}
	if err := descriptorcache.Save(ctx, s.index, next); err != nil {
		// Persisted on disk but cache write failed. The next
		// OpenStore will rebuild the cache from Location, so
		// this is recoverable; surface the error so the caller
		// knows the operation was not fully successful.
		return fmt.Errorf("save L2 cache: %w", err)
	}
	s.desc = next
	return nil
}
