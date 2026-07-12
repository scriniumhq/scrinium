package store

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/storeconfig"
	"scrinium.dev/event"
)

// Liveness sentinel (ADR-111). An open Store instance can outlive its
// physical store indefinitely: the sqlite index keeps answering from an
// open fd of an unlinked file, and surfaces that serve listings from
// the index/projection never touch the driver — a deleted store keeps
// "working" until restart. The sentinel closes that blindness: a
// periodic tick Stats-and-reads the primary descriptor cell and
// compares the store_id with the one fixed at open.
//
// Loss (descriptor unreadable) or substitution (store_id mismatch)
// flips the instance into MaintenanceModeOffline through the same
// machinery as SetMaintenanceMode, with sentinel provenance: an
// operator-set Offline is never touched, and the sentinel's own
// Offline self-heals when the descriptor comes back with the SAME
// store_id (a transient mount blip must not require a stack restart).
// A substituted store never self-heals — trust is re-established only
// by reopening.

// defaultLivenessInterval is the probe period when the caller does not
// override it. Cheap: one driver Stat + one small read per tick.
const defaultLivenessInterval = 5 * time.Second

// probeTimeout bounds a single probe so a hung network filesystem
// cannot wedge the sentinel goroutine between ticks.
const probeTimeout = 3 * time.Second

// startLiveness launches the sentinel goroutine. interval semantics:
// 0 → defaultLivenessInterval; negative → sentinel disabled (tests
// that construct partial stores, or hosts that run their own probe).
// Called at the successful tail of OpenStore/InitStore; stopped by
// Close via stopLiveness.
func (s *store) startLiveness(interval time.Duration) {
	if interval < 0 {
		return
	}
	if interval == 0 {
		interval = defaultLivenessInterval
	}
	s.livenessStop = make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-s.livenessStop:
				return
			case <-t.C:
				s.probeLiveness()
			}
		}
	}()
}

// stopLiveness terminates the sentinel goroutine. Safe on a store that
// never started one (nil channel) and idempotent (sync.Once).
func (s *store) stopLiveness() {
	if s.livenessStop == nil {
		return
	}
	s.livenessOnce.Do(func() { close(s.livenessStop) })
}

// probeLiveness runs one probe: read the primary descriptor cell,
// verify store_id, transition accordingly. Exported to tests via the
// ticker only — the probe itself takes no parameters and derives its
// own bounded context.
func (s *store) probeLiveness() {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	desc, err := descriptor.Read(ctx, s.drv, s.hashes)
	switch {
	case err != nil:
		// Do not distinguish not-found from I/O failure: either way
		// the world under the instance is not answering as a store.
		// Context cancellation is the one exception — a timed-out
		// probe on a slow mount is not evidence of loss.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return
		}
		s.sentinelLost("descriptor unreadable: " + err.Error())
	case desc.StoreID != s.storeID:
		s.sentinelSubstituted(desc.StoreID)
	default:
		s.sentinelHealthy()
		s.refreshGovernance(ctx)
	}
}

// refreshGovernance is the second consumer of the liveness tick
// (ADR-110, INV-110-7): a cheap max-seq probe of store.config; when
// another host published a new version (UpdateConfig writes the file,
// not a message), the in-memory active config is re-read and swapped,
// so agents and the Delete path — every snapshotConfig reader — see
// fresh governance defaults within a tick instead of "until restart".
// The local UpdateConfig path bumps lastConfigSeq itself, so its own
// write never looks like an external change here.
func (s *store) refreshGovernance(ctx context.Context) {
	seq, found, err := storeconfig.ActiveSeq(ctx, s.drv)
	if err != nil || !found {
		return // liveness already vouched for the world; stay quiet
	}
	s.cfgMu.RLock()
	known := s.lastConfigSeq
	s.cfgMu.RUnlock()
	if seq == known {
		return
	}

	cfg, readSeq, err := storeconfig.Read(ctx, s.drv, s.hashes)
	if err != nil {
		s.componentLogger("store").LogAttrs(ctx, slog.LevelWarn,
			"config freshness: new version detected but unreadable",
			storeIDAttr(s), slog.Uint64("seq", seq), slog.String("err", err.Error()))
		return
	}
	cfg = config.ApplyDefaults(cfg)
	if err := config.ValidateImmutable(cfg); err != nil {
		s.componentLogger("store").LogAttrs(ctx, slog.LevelWarn,
			"config freshness: new version invalid, keeping current",
			storeIDAttr(s), slog.Uint64("seq", readSeq), slog.String("err", err.Error()))
		return
	}

	s.cfgMu.Lock()
	if readSeq <= s.lastConfigSeq { // lost the race to a local UpdateConfig
		s.cfgMu.Unlock()
		return
	}
	s.activeConfig = cfg
	s.lastConfigSeq = readSeq
	s.cfgMu.Unlock()

	s.componentLogger("store").LogAttrs(ctx, slog.LevelInfo,
		"config refreshed from disk (external admin change)",
		storeIDAttr(s), slog.Uint64("seq", readSeq))
	s.publish(event.EventConfigUpdated, event.ConfigUpdatedPayload{Config: cfg})
}

// sentinelLost transitions to Offline with sentinel provenance. An
// Offline set by the operator (SetMaintenanceMode) is left alone.
func (s *store) sentinelLost(reason string) {
	s.stateMu.Lock()
	if s.maintenance == domain.MaintenanceModeOffline && !s.offlineBySentinel {
		s.stateMu.Unlock() // operator's Offline — not ours to manage
		return
	}
	already := s.offlineBySentinel
	s.maintenance = domain.MaintenanceModeOffline
	s.offlineBySentinel = true
	s.stateMu.Unlock()

	if already {
		return // still lost; logged and published on the first flip
	}
	s.componentLogger("store").LogAttrs(context.Background(), slog.LevelWarn,
		"liveness lost: store offline", storeIDAttr(s), slog.String("reason", reason))
	s.publish(event.EventStoreDegraded, event.StoreDegradedPayload{Reason: reason})
}

// sentinelSubstituted is the sticky variant of loss: a DIFFERENT store
// answered at our path. No self-heal ever — writing into a substituted
// store is worse than writing into a void.
func (s *store) sentinelSubstituted(foreignID string) {
	s.stateMu.Lock()
	if s.maintenance == domain.MaintenanceModeOffline && !s.offlineBySentinel {
		s.stateMu.Unlock()
		return
	}
	already := s.substituted
	s.maintenance = domain.MaintenanceModeOffline
	s.offlineBySentinel = true
	s.substituted = true
	s.stateMu.Unlock()

	if already {
		return
	}
	s.componentLogger("store").LogAttrs(context.Background(), slog.LevelWarn,
		"liveness lost: store substituted, offline until reopen",
		storeIDAttr(s), slog.String("foreign_store_id", foreignID))
	s.publish(event.EventStoreDegraded, event.StoreDegradedPayload{
		Reason: "store substituted: foreign store_id " + foreignID})
}

// sentinelHealthy self-heals a sentinel-set Offline when the same
// store is answering again. Substitution and operator Offline are
// never healed here.
func (s *store) sentinelHealthy() {
	s.stateMu.Lock()
	if !s.offlineBySentinel || s.substituted {
		s.stateMu.Unlock()
		return
	}
	s.maintenance = domain.MaintenanceModeNone
	s.offlineBySentinel = false
	s.stateMu.Unlock()

	s.componentLogger("store").LogAttrs(context.Background(), slog.LevelInfo,
		"liveness restored: store back online", storeIDAttr(s))
	s.publish(event.EventStoreRecovered, event.StoreRecoveredPayload{
		Reason: "descriptor readable again, same store_id"})
}
