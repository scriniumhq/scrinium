package core

import (
	"context"
	"fmt"
	"slices"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
)

// admin_config.go — AdminStore methods that read or mutate
// system.config: UpdateConfig and ConfigHistory. Companion to
// crypto_admin.go (which holds the AdminStore methods that touch
// the descriptor's crypto Paranoid).
//
// The `Config()` accessor stays on store_impl.go alongside the
// other trivial getters (`State`, `Capabilities`); it is a one-line
// snapshot that does not warrant its own file.

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
func (s *store) UpdateConfig(ctx context.Context, cfg domain.StoreConfig) error {
	if err := s.enterWrite(ctx); err != nil {
		return err
	}

	current := s.snapshotConfig()
	requested := applyConfigDefaults(cfg)

	if err := validateImmutableConfig(requested); err != nil {
		return fmt.Errorf("core.UpdateConfig: %w", err)
	}
	// validateAgainstActiveConfig compares requested to current on
	// every immutable field; mutable fields pass through. Same
	// contract as OpenStore's WithConfig check.
	if err := validateAgainstActiveConfig(requested, current); err != nil {
		return fmt.Errorf("core.UpdateConfig: %w", err)
	}
	// DeletionPolicyLock guard: once locked, NoDelete cannot be
	// dropped through UpdateConfig. The lock flag itself is
	// immutable (caught by validateAgainstActiveConfig above).
	if current.DeletionPolicyLock &&
		current.DeletionPolicy == domain.DeletionPolicyNoDelete &&
		requested.DeletionPolicy != domain.DeletionPolicyNoDelete {
		return fmt.Errorf("%w: DeletionPolicy locked at NoDelete by InitStore",
			errs.ErrConfigMismatch)
	}

	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if _, err := writeSystemConfig(ctx, s.drv, s.index, s.hashes, requested); err != nil {
		return fmt.Errorf("core.UpdateConfig: %w", err)
	}
	s.activeConfig = requested
	return nil
}

// ConfigHistory returns every system.config snapshot, the active
// one first, the rest sorted by CreatedAt descending. Snapshots
// where the layout is not Inline are skipped — the format requires
// inline storage for the config payload, anything else is
// corruption (caller will hit it through other code paths).
//
// The "active first" rule is per docs/4 §4.4: a rollback through
// UpdateConfig produces a fresh snapshot whose CreatedAt is newer
// than what is being rolled back to, and the disk pointer follows
// the rollback. Sorting purely by time would put the "discarded"
// version first; promoting the pointer's target keeps the result
// honest about which config is in effect.
func (s *store) ConfigHistory(ctx context.Context) ([]domain.StoreConfig, error) {
	if err := s.enterRead(ctx); err != nil {
		return nil, err
	}

	currentID, err := readSystemConfigPointer(ctx, s.drv, s.hashes)
	if err != nil {
		return nil, fmt.Errorf("core.ConfigHistory: %w", err)
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
	listErr := s.index.ListByNamespace(ctx, domain.NamespaceSystemConfig, func(m domain.Manifest) error {
		cfg, err := loadSystemConfigByID(ctx, s.drv, s.hashes, m.ArtifactID)
		if err != nil {
			return fmt.Errorf("decode %s: %w", m.ArtifactID, err)
		}
		entries = append(entries, entry{
			id:        m.ArtifactID,
			cfg:       applyConfigDefaults(cfg),
			createdAt: m.CreatedAt,
		})
		return nil
	})
	if listErr != nil {
		return nil, fmt.Errorf("core.ConfigHistory: list by namespace: %w", listErr)
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
		// Pointer points at an artifact WalkSystem did not yield.
		// On a healthy Store this cannot happen — bootstrap reads
		// the same pointer and would have refused. Treat as
		// dangling and surface the standard error.
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
