package store

import (
	"context"
	"fmt"
	"log/slog"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/engine/store/internal/storeconfig"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// Config returns a snapshot of the active StoreConfig. A pure
// in-memory reader, so it skips the enter* gate (like State /
// Capabilities).
func (s *store) Config() domain.StoreConfig {
	return s.snapshotConfig()
}

// snapshotConfig returns the active config under cfgMu.RLock(). The
// single in-memory read used by Config() and by every method that
// needs the current config without re-reading disk.
//
// Deliberately WITHOUT the session overlay: Config() is the admin
// view of the store's defaults, and a Config()→tweak→UpdateConfig
// round-trip must never persist session values into them. Class-III
// consumers on the data paths use sessionConfig() instead (ADR-110).
func (s *store) snapshotConfig() domain.StoreConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.activeConfig
}

// sessionConfig is the connection's effective config: the active
// defaults with the class-III session overlay laid over them
// (ADR-110). Used by the data paths that consume class-III fields
// (Put, Get, headless writes); governance consumers (Delete, agents)
// stay on snapshotConfig — an overlay can never soften governance by
// construction (it carries class III only).
func (s *store) sessionConfig() domain.StoreConfig {
	return config.MergeSession(s.snapshotConfig(), s.sessionOverlay)
}

// UpdateConfig swaps the active StoreConfig. Only mutable fields
// can change; immutable mismatches return errs.ErrConfigMismatch.
//
// On-disk effect: a new system/config version is published by
// claiming the next seq (ADR-85); the active config is the highest
// seq. The in-memory active config is swapped only after the write
// succeeds, so Config() never returns a value that disagrees with
// the active on-disk version.
//
// Concurrency: the disk write and the in-memory swap are both
// performed under cfgMu.Lock(). Two parallel UpdateConfig calls
// serialise here; the last writer wins, but each transaction is
// internally consistent. Readers (Config, snapshotConfig) take
// cfgMu.RLock() and so block only for the brief swap window.
func (s *store) UpdateConfig(ctx context.Context, cfg domain.StoreConfig) error {
	if err := s.enterWrite(ctx); err != nil {
		return err
	}

	current := s.snapshotConfig()
	requested := config.ApplyDefaults(cfg)

	if err := config.ValidateImmutable(requested); err != nil {
		return fmt.Errorf("store.UpdateConfig: %w", err)
	}
	// ValidateAgainstActive compares requested to current on every
	// immutable field; mutable fields pass through. Same contract as
	// OpenStore's WithConfig check.
	if err := config.ValidateAgainstActive(requested, current); err != nil {
		return fmt.Errorf("store.UpdateConfig: %w", err)
	}
	// DeletionPolicyLock guard: once locked, NoDelete cannot be
	// dropped through UpdateConfig. The lock flag itself is
	// immutable (caught by ValidateAgainstActive above).
	if current.DeletionPolicyLock &&
		current.DeletionPolicy == domain.DeletionPolicyNoDelete &&
		requested.DeletionPolicy != domain.DeletionPolicyNoDelete {
		return fmt.Errorf("%w: DeletionPolicy locked at NoDelete by InitStore",
			errs.ErrConfigMismatch)
	}

	s.cfgMu.Lock()
	seq, err := storeconfig.Write(ctx, s.drv, s.hashes, requested)
	if err != nil {
		s.cfgMu.Unlock()
		return fmt.Errorf("store.UpdateConfig: %w", err)
	}
	s.activeConfig = requested
	s.lastConfigSeq = seq
	s.cfgMu.Unlock()

	// Lock-free (cfgMu released): the active config was swapped on disk
	// and in memory. Info — a config change is operator-relevant.
	s.componentLogger("store").LogAttrs(ctx, slog.LevelInfo, "config updated",
		storeIDAttr(s), manifestCryptoAttr(requested.ManifestCrypto))

	// Outcome event, emitted outside cfgMu: the active config now
	// equals `requested`. Subscribers that need the prior value
	// cache the snapshot from an earlier EventConfigUpdated.
	s.publish(event.EventConfigUpdated, event.ConfigUpdatedPayload{Config: requested})
	return nil
}

// ConfigHistory returns every system/config snapshot, newest first.
// Under the seq model (ADR-85) the active config is simply the highest
// version, so "newest first" already puts the in-effect config at
// index 0 — no pointer reconciliation is needed. A rollback is itself
// published as a new max-seq copy, so it too surfaces at index 0.
func (s *store) ConfigHistory(ctx context.Context) ([]domain.StoreConfig, error) {
	if err := s.enterRead(ctx); err != nil {
		return nil, err
	}
	hist, err := storeconfig.History(ctx, s.drv, s.hashes)
	if err != nil {
		return nil, fmt.Errorf("store.ConfigHistory: %w", err)
	}
	return hist, nil
}
