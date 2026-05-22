package store

// lifecycle.go — descriptor-related helpers shared by InitStore
// (lifecycle_init.go) and OpenStore (lifecycle_open.go). Splitting the constructors
// into their own files keeps each one navigable; the common
// machinery — building a *store, healing replicas, refreshing the
// descriptor cache, bootstrap-time Unlock — lives here so neither
// constructor reaches across into the other.

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/orphanscan"
	"scrinium.dev/engine/store/internal/systemstore"
)

// buildStore is the common tail shared by InitStore and OpenStore.
// It constructs the *store value, runs the bootstrap Orphan Scan,
// publishes the report, and transitions the Store into
// StateUnlocked. Errors are surfaced unwrapped — the caller adds
// its own "store.InitStore" / "store.OpenStore" prefix.
//
// Pre-conditions checked by the caller (not re-checked here):
//   - drv != nil
//   - idx != nil
//   - cfg has been defaulted and validated
//   - storeID is fresh (Init) or read from the descriptor (Open)
//
// When M2 lands the Locked → Bootstrapping → Unlocked transition
// (encrypted Stores), this helper is the single point that learns
// to wait for Unlock before flipping the state — both entry
// points then pick up the new flow without further changes.
func buildStore(
	ctx context.Context,
	o storeOptions,
	drv driver.Driver,
	idx coreapi.StoreIndex,
	cfg domain.StoreConfig,
	desc *descriptor.Descriptor,
	dek []byte,
) (*store, error) {
	_ = ctx // reserved for future bootstrap-time index probes
	s := &store{
		storeID:      desc.StoreID,
		drv:          drv,
		index:        idx,
		pub:          o.publisher,
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
	s.system = systemstore.New(drv, idx, o.hashRegistry, cfg,
		// ArtifactWriter: the inline-artifact write primitive lives in
		// store (shared with the config writer); systemstore calls it
		// through this closure, branching on skipIndex.
		func(ctx context.Context, ns string, sid domain.SessionID, payload []byte, hashAlgo string, skipIndex bool) (domain.ArtifactID, error) {
			if skipIndex {
				return writeInlineSystemArtifactUnindexed(ctx, drv, o.hashRegistry, ns, sid, payload, hashAlgo)
			}
			return writeInlineSystemArtifact(ctx, drv, idx, o.hashRegistry, ns, sid, payload, hashAlgo)
		},
		// InlineHandleFactory: inlineReadHandle is store-private (Get
		// path); systemstore builds handles through this closure.
		func(m domain.Manifest) coreapi.ReadHandle {
			return &inlineReadHandle{manifest: m, reader: bytes.NewReader(m.InlineBlob)}
		},
	)
	return s, nil
}

// unlockBootstrap completes the bootstrap-into-Unlocked transition
// shared by InitStore (always), the Plain-DEK OpenStore path,
// the AutoUnlock OpenStore path, and the deferred Store.Unlock
// path (1.2b.5).
//
// The caller has produced a *store in StateBootstrapping with
// the DEK already populated. unlockBootstrap runs the Orphan
// Scan per §10.2, publishes the report, and flips state to
// StateUnlocked atomically.
//
// Errors from the Orphan Scan propagate; the *store is left in
// StateBootstrapping. The caller decides whether to retry, fall
// back to Locked, or surface the failure.
func unlockBootstrap(ctx context.Context, s *store, pub coreapi.Publisher) error {
	report, err := orphanscan.RecoverOrphans(ctx, s.drv, s.index)
	if err != nil {
		return fmt.Errorf("orphan scan: %w", err)
	}
	// Record the scan timestamp per docs/2 §10.2 "Label". Best-effort:
	// SetMeta failure is appended to the report so observability sees
	// it, but does not block the transition — the cache key is a
	// diagnostic aid, not a liveness gate.
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
