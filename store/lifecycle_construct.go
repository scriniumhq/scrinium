package store

// Machinery shared by InitStore and OpenStore: building a *store,
// healing descriptor replicas, and the bootstrap-into-Unlocked
// transition. Kept here so neither constructor reaches into the other.

import (
	"context"
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/event"
	"scrinium.dev/store/driver"
	"scrinium.dev/store/index"
	"scrinium.dev/store/internal/artifactio"
	"scrinium.dev/store/internal/descriptor"
	"scrinium.dev/store/internal/orphanscan"
	"scrinium.dev/store/internal/reconcile"
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
) (*store, error) {
	s := &store{
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
	s.system = newSystemStore(drv, idx, o.hashRegistry, cfg,
		// ArtifactWriter: the inline-artifact write primitive lives in
		// store (shared with the config writer); systemstore calls it
		// through this closure, branching on skipIndex.
		func(ctx context.Context, ns string, sid domain.SessionID, payload []byte, hashAlgo string, skipIndex bool) (domain.ArtifactID, error) {
			if skipIndex {
				return writeInlineSystemArtifactUnindexed(ctx, drv, o.hashRegistry, ns, sid, payload, hashAlgo)
			}
			return writeInlineSystemArtifact(ctx, drv, idx, o.hashRegistry, ns, sid, payload, hashAlgo)
		},
		// InlineHandleFactory: systemstore builds inline handles through
		// this closure; the implementation lives in artifactio.
		func(m domain.Manifest) domain.ReadHandle {
			return artifactio.NewInlineHandle(m)
		},
		// Logger: the systemStore logs its own best-effort cleanup
		// failures (dropPredecessor) which have no caller to surface
		// them. resolveLogger was already applied to s.log in the
		// struct literal above; reuse it so the component tag is "store".
		s.log,
	)
	return s, nil
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
func unlockBootstrap(ctx context.Context, s *store, pub event.Publisher) error {
	report, err := orphanscan.RecoverOrphans(ctx, s.drv, s.index)
	if err != nil {
		return fmt.Errorf("orphan scan: %w", err)
	}
	// Record the scan timestamp. Best-effort: a SetMeta failure is
	// appended to the report for observability but does not block the
	// transition — the timestamp is a diagnostic aid, not a liveness gate.
	if setErr := s.index.SetMeta(ctx, "last_orphan_scan_at", time.Now().UTC().Format(time.RFC3339)); setErr != nil {
		report.Errors = append(report.Errors,
			fmt.Errorf("unlockBootstrap: persist last_orphan_scan_at: %w", setErr))
	}
	orphanscan.PublishOrphanReport(pub, report)

	s.stateMu.Lock()
	s.state = domain.StateUnlocked
	s.stateMu.Unlock()
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
