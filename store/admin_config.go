package store

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
	"scrinium.dev/store/internal/storeconfig"
)

// Config returns a snapshot of the active StoreConfig. A pure
// in-memory reader, so it skips the enter* gate (like State /
// Capabilities).
func (a adminFacet) Config() domain.StoreConfig {
	return a.snapshotConfig()
}

// snapshotConfig returns the active config under cfgMu.RLock(). The
// single in-memory read used by Config() and by every method that
// needs the current config without re-reading disk.
func (c *core) snapshotConfig() domain.StoreConfig {
	c.cfgMu.RLock()
	defer c.cfgMu.RUnlock()
	return c.activeConfig
}

// UpdateConfig swaps the active StoreConfig. Only mutable fields
// can change; immutable mismatches return errs.ErrConfigMismatch.
//
// On-disk effect: a new system.config inline artifact is written
// and system.config/current is atomically bumped to its
// ArtifactID. The in-memory active config is swapped only after
// both writes succeed, so Config() never returns a value that
// disagrees with the disk pointer.
//
// Concurrency: the disk write and the in-memory swap are both
// performed under cfgMu.Lock(). Two parallel UpdateConfig calls
// serialise here; the last writer wins, but each transaction is
// internally consistent. Readers (Config, snapshotConfig) take
// cfgMu.RLock() and so block only for the brief swap window.
func (a adminFacet) UpdateConfig(ctx context.Context, cfg domain.StoreConfig) error {
	if err := a.enterWrite(ctx); err != nil {
		return err
	}

	current := a.snapshotConfig()
	requested := storeconfig.ApplyDefaults(cfg)

	if err := storeconfig.ValidateImmutable(requested); err != nil {
		return fmt.Errorf("store.UpdateConfig: %w", err)
	}
	// ValidateAgainstActive compares requested to current on every
	// immutable field; mutable fields pass through. Same contract as
	// OpenStore's WithConfig check.
	if err := storeconfig.ValidateAgainstActive(requested, current); err != nil {
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

	a.cfgMu.Lock()
	if _, err := storeconfig.Write(ctx, a.drv, configWriter(a.drv, a.index, a.hashes), requested); err != nil {
		a.cfgMu.Unlock()
		return fmt.Errorf("store.UpdateConfig: %w", err)
	}
	a.activeConfig = requested
	a.cfgMu.Unlock()

	// Lock-free (cfgMu released): the active config was swapped on disk
	// and in memory. Info — a config change is operator-relevant.
	a.componentLogger("store").LogAttrs(ctx, slog.LevelInfo, "config updated",
		storeIDAttr(a.core), manifestCryptoAttr(requested.ManifestCrypto))
	return nil
}

// ConfigHistory returns every system.config snapshot: the active one
// first, the rest sorted by CreatedAt descending.
//
// The "active first" rule matters because a rollback through
// UpdateConfig produces a fresh snapshot whose CreatedAt is newer than
// the version it rolls back to, while the disk pointer follows the
// rollback. Sorting purely by time would put the discarded version
// first; promoting the pointer's target keeps the result honest about
// which config is in effect.
func (a adminFacet) ConfigHistory(ctx context.Context) ([]domain.StoreConfig, error) {
	if err := a.enterRead(ctx); err != nil {
		return nil, err
	}

	currentID, err := storeconfig.ReadPointer(ctx, a.drv, a.hashes)
	if err != nil {
		return nil, fmt.Errorf("store.ConfigHistory: %w", err)
	}

	// ListByNamespace yields manifest rows from the index — namespace,
	// CreatedAt, ArtifactID — but NOT the inline payload (which
	// lives only in the manifest file on disk; the index has no
	// column for it). Each entry therefore needs a second hop:
	// load the manifest file for its ArtifactID and unmarshal the
	// embedded StoreConfig payload.
	type entry struct {
		id        domain.ArtifactID
		cfg       domain.StoreConfig
		createdAt time.Time
	}
	var entries []entry
	listErr := a.index.ListByNamespace(ctx, domain.NamespaceSystemConfig, func(m domain.Manifest) error {
		cfg, err := storeconfig.LoadByID(ctx, a.drv, a.hashes, m.ArtifactID)
		if err != nil {
			return fmt.Errorf("decode %s: %w", m.ArtifactID, err)
		}
		entries = append(entries, entry{
			id:        m.ArtifactID,
			cfg:       storeconfig.ApplyDefaults(cfg),
			createdAt: m.CreatedAt,
		})
		return nil
	})
	if listErr != nil {
		return nil, fmt.Errorf("store.ConfigHistory: list by namespace: %w", listErr)
	}

	slices.SortStableFunc(entries, func(a, b entry) int {
		// Reverse chronological order: newest first.
		return b.createdAt.Compare(a.createdAt)
	})

	currentIdx := -1
	for i := range entries {
		if entries[i].id == currentID {
			currentIdx = i
			break
		}
	}
	if currentIdx < 0 {
		// Pointer points at an artifact the index did not yield. On a
		// healthy Store this cannot happen — bootstrap reads the same
		// pointer and would have refused. Surface the standard error.
		return nil, errs.ErrDanglingConfigPointer
	}

	out := make([]domain.StoreConfig, 0, len(entries))
	out = append(out, entries[currentIdx].cfg)
	for i, e := range entries {
		if i == currentIdx {
			continue
		}
		out = append(out, e.cfg)
	}
	return out, nil
}
