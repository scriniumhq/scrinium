package rebuild

import (
	"context"
	"fmt"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/agent/internal/lease"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// Run acquires the maintenance lease and rebuilds the index. Auto and
// Checkpoint take the checkpoint fast-path when a checkpoint exists and the
// index can restore one; Auto otherwise falls back to a full Location scan,
// and FullScan always scans.
func (a *rebuildAgent) run(ctx context.Context) (*domain.AgentResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start := time.Now()
	a.bus.Publish(event.Event{Type: event.EventAgentStarted, Payload: event.AgentStartedPayload{
		AgentType: "rebuild", StoreID: a.storeID, StartedAt: start,
	}})

	l, prev, err := lease.Acquire(ctx, a.drv, lease.Config{
		Path:      rebuildLeasePath,
		HostID:    a.hostID,
		AgentType: "rebuild",
		TTL:       a.cfg.LeaseTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("rebuild.Rebuild.Run: acquire lease: %w", err)
	}
	if prev != nil {
		a.bus.Publish(event.Event{Type: event.EventAgentStaleLease, Payload: event.LeaseTakeoverPayload{
			LeaseKey: rebuildLeasePath, PreviousHolder: prev.HostID,
			ExpiredAt: prev.ExpiresAt, TakenBy: a.hostID,
		}})
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	hbErr := make(chan error, 1)
	go func() { hbErr <- l.Heartbeat(runCtx) }()
	defer func() {
		cancel()
		if err := l.Release(context.WithoutCancel(ctx)); err != nil {
			a.Logger().Warn("lease release failed; lease will expire via TTL", "err", err)
		}
	}()

	// Catastrophic recovery: rewrite store.json from the Recovery Kit
	// before the scan, under the maintenance lease, when every
	// descriptor replica was lost. The scan then repopulates the index
	// from the manifests that survived alongside the blobs.
	if a.cfg.RecoveryKit != nil {
		if err := a.restoreDescriptor(runCtx); err != nil {
			a.bus.Publish(event.Event{Type: event.EventAgentFailed, Payload: event.AgentFailedPayload{
				AgentType: "rebuild", StoreID: a.storeID, Err: err,
			}})
			return nil, fmt.Errorf("rebuild.Rebuild.Run: recovery kit restore: %w", err)
		}
	}

	// Key material for decoding encrypted manifests read straight off the
	// Location. nil for an unencrypted Store — the scan then handles Plain
	// manifests only (encrypted ones are skipped, as before).
	keys := store.ManifestKeyProvider(a.store)
	if err := a.rebuildIndex(runCtx, keys); err != nil {
		a.bus.Publish(event.Event{Type: event.EventAgentFailed, Payload: event.AgentFailedPayload{
			AgentType: "rebuild", StoreID: a.storeID, Err: err,
		}})
		return nil, fmt.Errorf("rebuild.Rebuild.Run: %w", err)
	}

	// Surface a lease loss that aborted the scan.
	select {
	case herr := <-hbErr:
		if herr != nil && !agent.IsCtxErr(herr) {
			return nil, fmt.Errorf("rebuild.Rebuild.Run: lease lost: %w", herr)
		}
	default:
	}

	a.mu.Lock()
	a.stats.Duration = time.Since(start)
	final := a.stats
	a.mu.Unlock()

	res := &domain.AgentResult{
		// AgentType matches the registered kind and the agent's other
		// events (started/failed/stale-lease) so consumers can correlate a
		// rebuild's events by a single type. ("RebuildIndex" remains the
		// lease owner tag, a separate concern.)
		AgentType:   "rebuild",
		StoreID:     a.storeID,
		CompletedAt: time.Now(),
		Stats: map[string]int64{
			"manifests_scanned": final.ManifestsScanned,
			"manifests_indexed": final.ManifestsIndexed,
			"blobs_registered":  final.BlobsRegistered,
		},
	}
	a.bus.Publish(event.Event{Type: event.EventAgentCompleted, Payload: *res})
	return res, nil
}

// restoreDescriptor rewrites store.json (and its L1 shadow) from the
// Recovery Kit in the config, for the catastrophic case where every
// on-disk descriptor replica was lost. It records the outcome in stats
// (DescriptorRewrote). The kit-to-descriptor mapping and the two-replica
// write live in the store package (RestoreDescriptorFromRecoveryKit),
// which owns the descriptor and kit formats; the agent only sequences it
// under the maintenance lease ahead of the scan.
func (a *rebuildAgent) restoreDescriptor(ctx context.Context) error {
	info, err := store.RestoreDescriptorFromRecoveryKit(ctx, a.drv, a.cfg.RecoveryKit)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.stats.DescriptorRewrote = info.DescriptorWritten
	a.mu.Unlock()
	return nil
}

// rebuildIndex selects the strategy. Auto and Checkpoint attempt the
// checkpoint fast-path first; Auto falls back to a full scan when no
// checkpoint is available, Checkpoint errors, and FullScan always scans.
func (a *rebuildAgent) rebuildIndex(ctx context.Context, keys artifact.KeyProvider) error {
	if a.cfg.Source != RebuildSourceFullScan {
		used, err := a.tryCheckpointFastPath(ctx, keys)
		if err != nil {
			return err
		}
		if used {
			return nil
		}
		if a.cfg.Source == RebuildSourceCheckpoint {
			return fmt.Errorf("rebuild: Source=Checkpoint but no checkpoint is available: %w", errs.ErrNoCheckpoint)
		}
		// Auto: fall through to a full scan.
	}
	a.setSource(RebuildSourceFullScan)
	return a.scanManifests(ctx, keys, time.Time{})
}
