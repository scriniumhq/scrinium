package store

// Machinery shared by InitStore and OpenStore: building a *store,
// healing descriptor replicas, and the bootstrap-into-Unlocked
// transition. Kept here so neither constructor reaches into the other.

import (
	"context"
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/orphanscan"
	"scrinium.dev/engine/store/internal/reconcile"
	"scrinium.dev/event"
)

// buildStore constructs the *store value and wires the systemStore
// facade. The caller has already defaulted and validated cfg and
// supplied a non-nil drv and idx; this function does not re-check them.
func buildStore(
	ctx context.Context,
	o storeOptions,
	drv driver.Driver,
	idx index.StoreIndex,
	cfg domain.StoreConfig,
	desc *descriptor.Descriptor,
	dek []byte,
) (*core, error) {
	c := &core{
		storeID:      desc.StoreID,
		drv:          drv,
		index:        idx,
		pub:          o.publisher,
		log:          resolveLogger(o.logger),
		activeConfig: cfg,
		state:        domain.StateBootstrapping,
		hashes:       o.hashRegistry,
		transformers: o.readRegistry,
		crypto: cryptoState{
			desc:        desc,
			dek:         dek,
			provider:    o.passphrase,
			keyResolver: o.keyResolver,
		},
	}
	// SystemStore facade over the pointer-free layout (ADR-85). It needs
	// only the driver, the hash registry, the active config (for its
	// immutable ContentHasher), and a logger — no StoreIndex and no write
	// indirection, since system artifacts are unindexed and the inline
	// write is self-contained in namedstore.
	c.system = newSystemStore(drv, o.hashRegistry, cfg, c.log)
	return c, nil
}

// unlockBootstrap completes the bootstrap-into-Unlocked transition
// shared by InitStore, both OpenStore paths, and the deferred
// Store.Unlock path. The caller has produced a *store in
// StateBootstrapping with the DEK populated; unlockBootstrap runs the
// Orphan Scan, publishes the report, and flips state to StateUnlocked.
//
// An Orphan Scan error propagates with the *store left in
// StateBootstrapping; the caller decides whether to retry, fall back to
// Locked, or surface the failure.
func unlockBootstrap(ctx context.Context, c *core, pub event.Publisher) error {
	report, err := orphanscan.RecoverOrphans(ctx, c.drv, c.index)
	if err != nil {
		return fmt.Errorf("orphan scan: %w", err)
	}
	// Record the scan timestamp. Best-effort: a SetMeta failure is
	// appended to the report for observability but does not block the
	// transition — the timestamp is a diagnostic aid, not a liveness gate.
	if setErr := c.index.SetMeta(ctx, "last_orphan_scan_at", time.Now().UTC().Format(time.RFC3339)); setErr != nil {
		report.Errors = append(report.Errors,
			fmt.Errorf("unlockBootstrap: persist last_orphan_scan_at: %w", setErr))
	}
	orphanscan.PublishOrphanReport(pub, report)

	c.stateMu.Lock()
	c.state = domain.StateUnlocked
	c.stateMu.Unlock()
	return nil
}

// healReplicas applies Reconcile's repair action: writes the
// damaged or missing replica from the canonical descriptor.
// HealNone is a no-op; the four healing actions reduce to two
// distinct disk operations (write L0 only, write L1 only) since
// the canonical content already lives on the surviving side.
func healReplicas(ctx context.Context, drv driver.Driver, canonical *descriptor.Descriptor, action reconcile.Action) error {
	switch action {
	case reconcile.HealNone:
		return nil
	case reconcile.HealL0FromL1, reconcile.HealBothFromL1:
		// HealL0FromL1: L0 was missing/corrupted, rewrite it.
		// HealBothFromL1: sequence-divergence, L1 won, rewrite L0.
		// Same disk operation; distinct names preserve diagnostic
		// detail in logs.
		return descriptor.WriteReplica(ctx, drv, canonical, descriptor.L0)
	case reconcile.HealL1FromL0, reconcile.HealBothFromL0:
		return descriptor.WriteReplica(ctx, drv, canonical, descriptor.L1)
	default:
		return fmt.Errorf("core: unknown ReconcileAction %d", int(action))
	}
}
