package store

import (
	"context"
	"fmt"
	"log/slog"

	"scrinium.dev/domain"
)

// admin_crypto.go — the AdminStore crypto operations. Each is a thin
// orchestration shell: it applies the state-machine gate, performs the
// state transition and logging, and delegates the key material work
// (prompting, KEK derivation, DEK wrapping, descriptor persist) to the
// crypto.State held on the core. The mechanics and the mutex that guards
// the key material live in engine/store/internal/crypto; this file owns
// only the surrounding state-machine concerns.
//
// Locking: the gates and transitions here take stateMu; the crypto.State
// methods take their own mutex internally and release it before
// returning. The two mutexes are never held at once.

// Unlock transitions an encrypted Store from Locked to Unlocked.
// Idempotent in Unlocked.
func (a adminFacet) Unlock(ctx context.Context) error {
	if err := a.unlockEncrypted(ctx); err != nil {
		return err
	}
	// Info — an unlock is an operator-relevant access-boundary event. No
	// secret is logged; the DEK never appears.
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
	// Warn — KEK rotation invalidates the prior Recovery Kit, which the
	// operator must re-export; surfacing it above Info reduces the chance
	// of an orphaned kit. No key material is logged.
	a.componentLogger("store").LogAttrs(ctx, slog.LevelWarn, "KEK rotated; prior recovery kit invalidated",
		storeIDAttr(a.core))
	return nil
}

// SetPassphrase enables encryption on a Store initialised with a
// plaintext DEK. Refuses with errs.ErrPassphraseAlreadySet when the DEK
// is already wrapped — use RotateKEK then.
func (a adminFacet) SetPassphrase(ctx context.Context) error {
	if err := a.setPassphraseImpl(ctx); err != nil {
		return err
	}
	// Info — the Store transitioned from plaintext-DEK to
	// passphrase-wrapped.
	a.componentLogger("store").LogAttrs(ctx, slog.LevelInfo, "passphrase set; DEK now wrapped",
		storeIDAttr(a.core))
	return nil
}

// unlockEncrypted is the state-machine shell of Store.Unlock for an
// encrypted Store. enterAdmin allows Locked through (the whole point of
// Unlock is to leave Locked) while rejecting closed/corrupted/offline/
// bootstrapping. State checks happen before the provider is invoked, so
// an already-Unlocked Store does not prompt.
func (c *core) unlockEncrypted(ctx context.Context) error {
	if err := c.enterAdmin(ctx); err != nil {
		return err
	}

	// Claim the bootstrap transition. Only a Locked Store proceeds, and
	// exactly one caller wins the Locked→Bootstrapping move; a second
	// concurrent Unlock observes Bootstrapping and is refused (the Store
	// is mid-transition), an already-Unlocked Store is a no-op.
	c.stateMu.Lock()
	switch c.state {
	case domain.StateUnlocked:
		c.stateMu.Unlock()
		return nil
	case domain.StateLocked:
		c.state = domain.StateBootstrapping
		c.stateMu.Unlock()
	default:
		st := c.state
		c.stateMu.Unlock()
		return fmt.Errorf("store.Unlock: state %v rejects unlock", st)
	}

	// Unwrap the DEK (idempotent on the DEK; no re-prompt if already
	// held). On failure revert to Locked so a retry can re-prompt.
	if err := c.crypto.UnlockDEK(ctx, c.storeID); err != nil {
		c.stateMu.Lock()
		c.state = domain.StateLocked
		c.stateMu.Unlock()
		return fmt.Errorf("store.Unlock: %w", err)
	}

	// Bootstrap-into-Unlocked: same Orphan Scan path as the AutoUnlock
	// leg of OpenStore (unlockBootstrap flips state to Unlocked on
	// success). Failure wipes the just-unwrapped DEK and reverts to
	// Locked — holding the key in memory for a non-operational Store adds
	// risk without benefit.
	if err := unlockBootstrap(ctx, c, c.pub); err != nil {
		c.crypto.WipeDEK()
		c.stateMu.Lock()
		c.state = domain.StateLocked
		c.stateMu.Unlock()
		return fmt.Errorf("store.Unlock: %w", err)
	}
	return nil
}

// setPassphraseImpl is the state-machine shell of Store.SetPassphrase.
// It gates on enterWrite, refuses in Degraded (a descriptor rewrite on
// already-diverged replicas is unsafe until Auto-Heal reaches Unlocked),
// resolves the KDF cost from config, and delegates the wrap+persist to
// crypto.State.
func (c *core) setPassphraseImpl(ctx context.Context) error {
	if err := c.enterWrite(ctx); err != nil {
		return err
	}
	if st := c.currentState(); st == domain.StateDegraded {
		return fmt.Errorf("store.SetPassphrase: state %v rejects write; wait for Auto-Heal", st)
	}
	// Cost: from active config if the caller specified KDFParams there;
	// otherwise WrapDEK's default. The descriptor's KDFParams is nil for a
	// Plain Store, so it cannot be the source.
	var cost domain.KDFParams
	if cfg := c.snapshotConfig(); cfg.KDFParams != nil {
		cost = *cfg.KDFParams
	}
	if err := c.crypto.SetPassphrase(ctx, c.storeID, cost); err != nil {
		return fmt.Errorf("store.SetPassphrase: %w", err)
	}
	return nil
}

// rotateKEKImpl is the state-machine shell of Store.RotateKEK. Same gate
// and Degraded refusal as SetPassphrase; the current-passphrase proof,
// re-wrap, and persist live in crypto.State.
func (c *core) rotateKEKImpl(ctx context.Context) error {
	if err := c.enterWrite(ctx); err != nil {
		return err
	}
	if st := c.currentState(); st == domain.StateDegraded {
		return fmt.Errorf("store.RotateKEK: state %v rejects write; wait for Auto-Heal", st)
	}
	if err := c.crypto.RotateKEK(ctx, c.storeID); err != nil {
		return fmt.Errorf("store.RotateKEK: %w", err)
	}
	return nil
}

// exportRecoveryKitImpl is the state-machine shell of
// Store.ExportRecoveryKit. It gates on enterRead, requires Unlocked or
// Degraded, and delegates kit assembly to crypto.State.
func (c *core) exportRecoveryKitImpl(ctx context.Context) ([]byte, error) {
	if err := c.enterRead(ctx); err != nil {
		return nil, err
	}
	switch c.currentState() {
	case domain.StateUnlocked, domain.StateDegraded:
		// OK
	default:
		return nil, fmt.Errorf("store.ExportRecoveryKit: state %v rejects export",
			c.currentState())
	}
	kit, err := c.crypto.RecoveryKit()
	if err != nil {
		return nil, fmt.Errorf("store.ExportRecoveryKit: %w", err)
	}
	return kit, nil
}
