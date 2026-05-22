package store

import (
	"context"
	"errors"
	"fmt"
	"os"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/descriptorcache"
	"scrinium.dev/engine/store/internal/keyring"
	"scrinium.dev/engine/store/internal/storeconfig"
)

// OpenStore opens an existing Store at the Location served by drv.
//
// Behaviour:
//  1. Read both descriptor replicas (L0 store.json, L1
//     .store.backup.json) per §10.1.5 and reconcile them. If
//     both are absent → errs.ErrStoreNotFound. If both are
//     unrecoverable (corrupted, or one corrupted + one absent)
//     → errs.ErrStoreCorrupted. If both are valid but content
//     differs at the same Sequence →
//     errs.ErrDescriptorSplitBrain. Otherwise the canonical
//     replica is selected (sequence-wins; equal-content
//     short-circuit) and any heal action is performed against
//     the Driver before proceeding.
//  2. Reconcile the L2 cache (store_meta) with the canonical
//     descriptor. The cache is rewritten when absent, when its
//     checksum diverges, or when its load fails — Location is
//     the source of truth, the cache is a fast-start aid.
//  3. Validate that a StoreIndex was provided via WithStoreIndex.
//     core never opens an index itself; the caller is responsible
//     for the dependency. Missing → error.
//  4. Load the active StoreConfig from system.config/current.
//     Defaults are applied; immutable parameters are validated.
//  5. Validate WithConfig (when supplied) against the active
//     config: immutable mismatch → errs.ErrConfigMismatch.
//     A caller without WithConfig accepts the on-disk config
//     as-is — a legitimate scenario for diagnostic tools and
//     projection-only consumers.
//  6. State machine and bootstrap:
//     - DEKEncrypted=false: Plain DEK in descriptor, used
//     directly. Run Orphan Scan, transition to
//     StateUnlocked.
//     - DEKEncrypted=true + WithAutoUnlock: invoke the
//     configured PassphraseProvider with Reason="unlock",
//     unwrap DEK, run Orphan Scan, transition to
//     StateUnlocked.
//     - DEKEncrypted=true without WithAutoUnlock: skip
//     Orphan Scan (the index walk needs the DEK in M2.3+
//     for Paranoid manifests; until then it would still
//     succeed for Plain manifests, but we treat the state
//     uniformly), transition to StateLocked. The next
//     Store.Unlock call completes bootstrap.
//
// Sealed and Paranoid manifest crypto are still rejected
// pending M2.3. The split between "encrypted DEK" (this pack)
// and "encrypted manifests" (next milestone) is deliberate:
// they are independent axes of the configuration and a Store
// with a passphrase-protected DEK plus Plain manifests is a
// useful intermediate.
//
// What does NOT happen yet (planned milestones in parens):
//   - location.lock acquisition / lease model (M3.1).
//   - StoreIndex schema cross-check against descriptor (M3.4).
func OpenStore(ctx context.Context, drv driver.Driver, opts ...StoreOption) (coreapi.Store, error) {
	if drv == nil {
		return nil, errors.New("store.OpenStore: nil driver")
	}

	// wrap is the local error-prefix closure for this function. The
	// 13+ fmt.Errorf("store.OpenStore: ...: %w") sites threaded the
	// same prefix manually; centralising it here means a single
	// edit if the prefix ever changes.
	wrap := func(stage string, err error) error {
		if stage == "" {
			return fmt.Errorf("store.OpenStore: %w", err)
		}
		return fmt.Errorf("store.OpenStore: %s: %w", stage, err)
	}

	// --- Resolve options ---

	o := storeOptions{}
	for _, fn := range opts {
		fn(&o)
	}

	// --- StoreIndex dependency ---
	//
	// Required up-front: readSystemConfig and the L2 cache need
	// it; refusing here gives a clearer error than failing later.

	idx := o.storeIndex
	if idx == nil {
		return nil, fmt.Errorf(
			"store.OpenStore: WithStoreIndex is required (see DI Example)")
	}
	if o.hashRegistry == nil {
		return nil, fmt.Errorf(
			"store.OpenStore: WithHashRegistry is required to read system.config")
	}

	// --- Read both descriptor replicas; reconcile ---

	l0, l1, l0s, l1s, err := descriptor.ReadBoth(ctx, drv)
	if err != nil {
		// Non-recoverable I/O error from the Driver — propagate.
		return nil, wrap("read descriptor", err)
	}
	rec, err := descriptor.Reconcile(l0, l0s, l1, l1s)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, errs.ErrStoreNotFound
	case errors.Is(err, errs.ErrStoreCorrupted):
		return nil, errs.ErrStoreCorrupted
	case err != nil:
		// Split-brain, malformed-replica branches — pass through
		// unchanged so callers can identify by errors.Is.
		return nil, wrap("", err)
	}
	desc := rec.Canonical

	// --- Heal divergent replicas ---
	//
	// Reconcile selected the canonical descriptor; if the on-disk
	// state diverges from it, write the side that needs updating.
	// Each heal is a single atomic Put; a crash mid-heal leaves
	// the same divergence we are recovering from now, and the
	// next OpenStore re-applies the same step. Idempotent.
	if err := healReplicas(ctx, drv, desc, rec.Action); err != nil {
		return nil, wrap("", err)
	}

	// --- Reconcile L2 cache with canonical descriptor ---
	//
	// Location is the source of truth; the cache is a fast-start
	// aid only. Save when absent, when corrupted (load returned
	// an error), or when checksum diverges. Read errors are
	// non-fatal — we always have the canonical to fall back to.
	if err := descriptorcache.Refresh(ctx, idx, desc); err != nil {
		return nil, wrap("refresh L2 cache", err)
	}

	// --- Load the active StoreConfig from system.config/current ---

	active, err := storeconfig.Read(ctx, drv, o.hashRegistry)
	if err != nil {
		return nil, wrap("read system.config", err)
	}
	active = storeconfig.ApplyDefaults(active)
	if err := storeconfig.ValidateImmutable(active); err != nil {
		return nil, fmt.Errorf("%w: system.config produced invalid config: %v",
			errs.ErrStoreCorrupted, err)
	}

	// --- Validate WithConfig against the active config ---

	if o.cfg != nil {
		if err := storeconfig.ValidateAgainstActive(*o.cfg, active); err != nil {
			return nil, err
		}
	}

	// --- Branch on DEK protection state ---

	if !desc.DEKEncrypted {
		// Plain DEK: descriptor carries the raw key. Bootstrap
		// proceeds straight to Unlocked.
		s, err := buildStore(ctx, o, drv, idx, active, desc, desc.DEK)
		if err != nil {
			return nil, wrap("", err)
		}
		s.promoteKeyResolverIfDefault()
		if err := unlockBootstrap(ctx, s, o.publisher); err != nil {
			return nil, wrap("", err)
		}
		return s, nil
	}

	// Encrypted DEK below.

	if !o.autoUnlock {
		// Stop in StateLocked — caller will follow up with
		// Store.Unlock. Orphan Scan is deferred until DEK is
		// available so the locked Store is a strictly minimal
		// in-memory shell.
		s, err := buildStore(ctx, o, drv, idx, active, desc, nil /* dek */)
		if err != nil {
			return nil, wrap("", err)
		}
		s.stateMu.Lock()
		s.state = domain.StateLocked
		s.stateMu.Unlock()
		return s, nil
	}

	// AutoUnlock: invoke the provider, derive KEK, unwrap DEK.
	if o.passphrase == nil {
		return nil, fmt.Errorf("store.OpenStore: %w: WithAutoUnlock requires WithPassphrase",
			errs.ErrPassphraseRequired)
	}
	passphrase, err := callProvider(ctx, o.passphrase, PassphraseHint{
		StoreID: desc.StoreID,
		Reason:  "unlock",
	})
	if err != nil {
		return nil, wrap("", err)
	}
	if desc.KDFParams == nil {
		// Defensive: a descriptor with DEKEncrypted=true must
		// have KDFParams (Validate enforces). Reaching this
		// branch means the Validate contract has drifted.
		aead.Wipe(passphrase)
		return nil, fmt.Errorf("%w: descriptor reports DEKEncrypted=true without KDFParams",
			errs.ErrStoreCorrupted)
	}
	dek, err := keyring.UnwrapDEK(desc.DEK, *desc.KDFParams, passphrase)
	aead.Wipe(passphrase)
	if err != nil {
		return nil, wrap("", err)
	}

	s, err := buildStore(ctx, o, drv, idx, active, desc, dek)
	if err != nil {
		aead.Wipe(dek)
		return nil, wrap("", err)
	}
	s.promoteKeyResolverIfDefault()
	if err := unlockBootstrap(ctx, s, o.publisher); err != nil {
		return nil, wrap("", err)
	}
	return s, nil
}
