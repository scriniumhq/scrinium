package rebuild

import (
	"context"
	"fmt"
	"time"

	"scrinium.dev/engine/agent"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/namedstore"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// rebuildCore rebuilds the index — no lifecycle; agent.RunLeased
// (ADR-94) owns lease/events/state. Auto and Checkpoint take the
// checkpoint fast-path when a checkpoint exists and the index can restore
// one; Auto otherwise falls back to a full Location scan, and FullScan
// always scans.
func (a *rebuildAgent) rebuildCore(ctx context.Context) (map[string]int64, error) {
	start := time.Now()

	// Catastrophic recovery: rewrite store.json from the Recovery Kit
	// before the scan, under the maintenance lease, when every
	// descriptor replica was lost. The scan then repopulates the index
	// from the manifests that survived alongside the blobs.
	if a.cfg.RecoveryKit != nil {
		if err := a.restoreDescriptor(ctx); err != nil {
			return nil, fmt.Errorf("rebuild.Rebuild.Run: recovery kit restore: %w", err)
		}
	}

	// Key material for decoding encrypted manifests read straight off the
	// Location. nil for an unencrypted Store — the scan then handles Plain
	// manifests only (encrypted ones are skipped, as before).
	keys := store.ManifestKeyProvider(a.store)
	if err := a.rebuildIndex(ctx, keys); err != nil {
		return nil, fmt.Errorf("rebuild.Rebuild.Run: %w", err)
	}

	a.mu.Lock()
	a.stats.Duration = time.Since(start)
	final := a.stats
	a.mu.Unlock()

	return map[string]int64{
		"manifests_scanned": final.ManifestsScanned,
		"manifests_indexed": final.ManifestsIndexed,
		"blobs_registered":  final.BlobsRegistered,
	}, nil
}

// maintenanceSpec is the RunLeased configuration: the rebuild lease
// (always taken) and the one-shot terminal (EventAgentCompleted on
// success). The lease-owner tag and the event AgentType are both "rebuild".
func (a *rebuildAgent) maintenanceSpec() agent.MaintenanceSpec {
	return agent.MaintenanceSpec{
		AgentType:    "rebuild",
		StoreID:      a.storeID,
		Lease:        namedstore.Config{Path: rebuildLeasePath, HostID: a.hostID, AgentType: "rebuild", TTL: a.cfg.LeaseTTL},
		LeaseEnabled: true,
		Terminal:     event.EventAgentCompleted,
		TerminalMode: agent.TerminalOnSuccess,
		Bus:          a.bus,
		Driver:       a.drv,
	}
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
