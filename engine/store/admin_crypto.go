package store

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/store/internal/crypto"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/keyring"
	"scrinium.dev/errs"
)

// Unlock transitions an encrypted Store from Locked to Unlocked.
// Idempotent in Unlocked.
func (a adminFacet) Unlock(ctx context.Context) error {
	if err := a.unlockEncrypted(ctx); err != nil {
		return err
	}
	// Lock-free: unlockEncrypted has released crypto.mu. Info — an
	// unlock is an operator-relevant access-boundary event. No secret
	// is logged; the DEK never appears.
	a.componentLogger("store").LogAttrs(ctx, slog.LevelInfo, "store unlocked",
		storeIDAttr(a.core))
	return nil
}

// ExportRecoveryKit returns the current Recovery Kit. Available in
// Unlocked and Degraded.
func (a adminFacet) ExportRecoveryKit(ctx context.Context) ([]byte, error) {
	return a.exportRecoveryKitImpl(ctx)
}

// RotateKEK re-wraps the DEK under a new KEK. On-disk data is not
// rewritten; the prior Recovery Kit is invalidated.
func (a adminFacet) RotateKEK(ctx context.Context) error {
	if err := a.rotateKEKImpl(ctx); err != nil {
		return err
	}
	// Lock-free: rotateKEKImpl has released crypto.mu. Warn — KEK
	// rotation invalidates the prior Recovery Kit, which the operator
	// must re-export; surfacing it above Info reduces the chance of an
	// orphaned kit. No key material is logged.
	a.componentLogger("store").LogAttrs(ctx, slog.LevelWarn, "KEK rotated; prior recovery kit invalidated",
		storeIDAttr(a.core))
	return nil
}

// SetPassphrase enables encryption on a Store initialised with a
// plaintext DEK. Refuses with errs.ErrPassphraseAlreadySet when the
// DEK is already wrapped — use RotateKEK then.
func (a adminFacet) SetPassphrase(ctx context.Context) error {
	if err := a.setPassphraseImpl(ctx); err != nil {
		return err
	}
	// Lock-free: setPassphraseImpl has released crypto.mu. Info — the
	// Store transitioned from plaintext-DEK to passphrase-wrapped.
	a.componentLogger("store").LogAttrs(ctx, slog.LevelInfo, "passphrase set; DEK now wrapped",
		storeIDAttr(a.core))
	return nil
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
func (c *core) unlockEncrypted(ctx context.Context) error {
	// enterAdmin applies the closed/corrupted/offline/bootstrapping
	// gate but allows Locked through — the whole point of Unlock is
	// to leave Locked. Specific Unlocked-vs-Locked logic stays in
	// the state switch below.
	if err := c.enterAdmin(ctx); err != nil {
		return err
	}
	c.crypto.mu.Lock()
	defer c.crypto.mu.Unlock()

	// Idempotent fast path. Holds cryptoMu so a concurrent
	// SetPassphrase/RotateKEK cannot race with the State read.
	switch c.currentState() {
	case domain.StateUnlocked:
		return nil
	case domain.StateLocked:
		// Proceed below.
	default:
		return fmt.Errorf("store.Unlock: state %v rejects unlock", c.currentState())
	}

	// Plain DEK in Locked state would be a bug — Plain Stores
	// open to Unlocked unconditionally. Defensive surface; if
	// we hit it, the open-path invariant is broken.
	if c.crypto.desc == nil || !c.crypto.desc.DEKEncrypted || c.crypto.desc.KDFParams == nil {
		return fmt.Errorf("%w: descriptor in Locked state lacks crypto fields",
			errs.ErrStoreCorrupted)
	}

	provider := c.crypto.provider
	passphrase, err := crypto.CallProvider(ctx, provider, domain.PassphraseHint{
		StoreID: c.storeID,
		Reason:  "unlock",
	})
	if err != nil {
		return fmt.Errorf("store.Unlock: %w", err)
	}

	dek, err := keyring.UnwrapDEK(c.crypto.desc.DEK, *c.crypto.desc.KDFParams, passphrase)
	aead.Wipe(passphrase)
	if err != nil {
		return fmt.Errorf("store.Unlock: %w", err)
	}

	c.crypto.dek = dek
	c.crypto.promoteResolverIfDefault()

	// Bootstrap-into-Unlocked: same path as the AutoUnlock leg
	// of OpenStore. State first goes to Bootstrapping inside
	// unlockBootstrap (via the Orphan Scan run), then to
	// Unlocked. Failure leaves the Store in Bootstrapping; the
	// caller can retry by opening fresh.
	c.stateMu.Lock()
	c.state = domain.StateBootstrapping
	c.stateMu.Unlock()

	if err := unlockBootstrap(ctx, c, c.pub); err != nil {
		// Wipe the DEK we just unwrapped — the Store is not
		// safely operational, holding the key in memory adds
		// risk without benefit.
		aead.Wipe(c.crypto.dek)
		c.crypto.dek = nil
		c.stateMu.Lock()
		c.state = domain.StateLocked
		c.stateMu.Unlock()
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
func (c *core) setPassphraseImpl(ctx context.Context) error {
	if err := c.enterWrite(ctx); err != nil {
		return err
	}
	c.crypto.mu.Lock()
	defer c.crypto.mu.Unlock()
	// SetPassphrase rewrites the descriptor (Sequence+1) into both
	// replicas. In Degraded the replicas are already out of sync —
	// piling another write on top is unsafe until Auto-Heal reaches
	// Unlocked. Refuse explicitly; checkWritable does not catch this
	// because Degraded is "API available, but consensus pending".
	if state := c.currentState(); state == domain.StateDegraded {
		return fmt.Errorf("store.SetPassphrase: state %v rejects write; wait for Auto-Heal", state)
	}
	if c.crypto.desc == nil {
		return fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if c.crypto.desc.DEKEncrypted {
		return errs.ErrPassphraseAlreadySet
	}
	if len(c.crypto.dek) == 0 {
		// A Plain Store must have a plaintext DEK after InitStore; if it
		// doesn't, the open-path invariant is broken.
		return fmt.Errorf("%w: Plain Store has no plaintext DEK", errs.ErrStoreCorrupted)
	}

	passphrase, err := crypto.CallProvider(ctx, c.crypto.provider, domain.PassphraseHint{
		StoreID: c.storeID,
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
	if cfg := c.snapshotConfig(); cfg.KDFParams != nil {
		cost = *cfg.KDFParams
	}

	wrapped, kdfParams, err := keyring.WrapDEK(c.crypto.dek, passphrase, cost)
	aead.Wipe(passphrase)
	if err != nil {
		return fmt.Errorf("store.SetPassphrase: %w", err)
	}

	next := *c.crypto.desc // shallow copy is fine; we'll replace pointer fields
	next.Sequence = c.crypto.desc.Sequence + 1
	next.DEK = wrapped
	next.DEKEncrypted = true
	next.KDFParams = &kdfParams

	if err := c.commitDescriptor(ctx, &next); err != nil {
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
func (c *core) rotateKEKImpl(ctx context.Context) error {
	if err := c.enterWrite(ctx); err != nil {
		return err
	}
	c.crypto.mu.Lock()
	defer c.crypto.mu.Unlock()
	// RotateKEK rewrites the descriptor (Sequence+1) into both
	// replicas. In Degraded the replicas are already out of sync —
	// piling another write on top is unsafe until Auto-Heal reaches
	// Unlocked. Refuse explicitly; checkWritable does not catch this
	// because Degraded is "API available, but consensus pending".
	if state := c.currentState(); state == domain.StateDegraded {
		return fmt.Errorf("store.RotateKEK: state %v rejects write; wait for Auto-Heal", state)
	}
	if c.crypto.desc == nil {
		return fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if !c.crypto.desc.DEKEncrypted {
		return fmt.Errorf("%w: Store has plaintext DEK; use SetPassphrase",
			errs.ErrPassphraseRequired)
	}
	if c.crypto.desc.KDFParams == nil {
		return fmt.Errorf("%w: encrypted descriptor lacks KDFParams",
			errs.ErrStoreCorrupted)
	}

	// First half: prove possession of the current passphrase
	// by unwrapping the DEK and matching it against c.crypto.dek. The
	// "unlock" reason mirrors Store.Unlock — host implementations
	// that retrieve passphrases from a keychain key off Reason and
	// expect the same lookup as a regular unlock.
	currentPass, err := crypto.CallProvider(ctx, c.crypto.provider, domain.PassphraseHint{
		StoreID: c.storeID,
		Reason:  "unlock",
	})
	if err != nil {
		return fmt.Errorf("store.RotateKEK: current passphrase: %w", err)
	}
	verified, err := keyring.UnwrapDEK(c.crypto.desc.DEK, *c.crypto.desc.KDFParams, currentPass)
	aead.Wipe(currentPass)
	if err != nil {
		return fmt.Errorf("store.RotateKEK: %w", err)
	}
	if !bytes.Equal(verified, c.crypto.dek) {
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
	newPass, err := crypto.CallProvider(ctx, c.crypto.provider, domain.PassphraseHint{
		StoreID: c.storeID,
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
		Time:    c.crypto.desc.KDFParams.Time,
		Memory:  c.crypto.desc.KDFParams.Memory,
		Threads: c.crypto.desc.KDFParams.Threads,
	}
	wrapped, kdfParams, err := keyring.WrapDEK(c.crypto.dek, newPass, cost)
	aead.Wipe(newPass)
	if err != nil {
		return fmt.Errorf("store.RotateKEK: %w", err)
	}

	next := *c.crypto.desc
	next.Sequence = c.crypto.desc.Sequence + 1
	next.DEK = wrapped
	next.KDFParams = &kdfParams
	// DEKEncrypted stays true; DEK bytes are only the wrapping change.

	if err := c.commitDescriptor(ctx, &next); err != nil {
		return fmt.Errorf("store.RotateKEK: %w", err)
	}

	// Note: c.crypto.dek does NOT change. The DEK is re-wrapped under
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
func (c *core) exportRecoveryKitImpl(ctx context.Context) ([]byte, error) {
	if err := c.enterRead(ctx); err != nil {
		return nil, err
	}
	c.crypto.mu.Lock()
	defer c.crypto.mu.Unlock()

	switch c.currentState() {
	case domain.StateUnlocked, domain.StateDegraded:
		// OK
	default:
		return nil, fmt.Errorf("store.ExportRecoveryKit: state %v rejects export",
			c.currentState())
	}

	if c.crypto.desc == nil {
		return nil, fmt.Errorf("%w: descriptor not loaded", errs.ErrStoreCorrupted)
	}
	if !c.crypto.desc.DEKEncrypted {
		return nil, fmt.Errorf("%w: Plain Store has no Recovery Kit",
			errs.ErrPassphraseRequired)
	}

	return crypto.BuildRecoveryKit(c.crypto.desc, c.crypto.desc.DEK)
}

// commitDescriptor is the shared tail of SetPassphrase and
// RotateKEK: persist the next descriptor (both replicas), refresh
// the L2 cache, and atomically swap s.crypto.desc.
//
// Caller must hold s.crypto.mu. On any error the caller's in-
// memory state (s.crypto.desc, s.crypto.dek) is left unchanged so a retry
// re-derives from the same starting point.
func (c *core) commitDescriptor(ctx context.Context, next *descriptor.Descriptor) error {
	if err := descriptor.Persist(ctx, c.drv, next); err != nil {
		return fmt.Errorf("persist descriptor: %w", err)
	}
	if err := descriptor.Save(ctx, c.index, next); err != nil {
		// Persisted on disk but cache write failed. The next
		// OpenStore will rebuild the cache from Location, so
		// this is recoverable; surface the error so the caller
		// knows the operation was not fully successful.
		return fmt.Errorf("save L2 cache: %w", err)
	}
	c.crypto.desc = next
	return nil
}
