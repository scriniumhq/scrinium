package store

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/namedstore"
	"scrinium.dev/engine/store/internal/artifactio"
)

// systemStore is the SystemStore facade over the pointer-free layout
// (ADR-85, engine/namedstore). Every system name maps
// to system/<name>/<seq>; the active version is max(seq); a write claims
// the next seq with an exclusive create. System artifacts are never
// indexed in StoreIndex and never written under manifests/ — they live
// in their own address space, so they are invisible to Store.Walk
// (handle-IS-NULL) by construction rather than by an index filter.
type systemStore struct {
	drv    driver.Driver
	hashes domain.HashRegistry
	cfg    domain.StoreConfig // immutable fields only (ContentHasher); see newSystemStore
	log    *slog.Logger
}

// defaultKeepVersions is the form a SystemArtifact takes when Keep is nil
// (unset): keep=1 — atomic, pointerless "latest" with no retained
// history. The safe default — forgetting Keep yields working versioned
// storage, never the exclusive-cell (lock) form, which requires an
// explicit KeepCell(). History (keep>1) is opt-in via KeepVersions(n);
// the config writer keeps its own history through namedstore directly.
const defaultKeepVersions = 1

// Compile-time check that the concrete type satisfies the contract.
var _ SystemStore = (*systemStore)(nil)

// newSystemStore wires the facade. It needs only the driver (the layout
// is on-disk), the hash registry (verify-on-read and the content hash of
// each write), the active config (for its immutable ContentHasher), and
// a logger for best-effort prune failures. No StoreIndex and no write
// indirection: the inline-manifest write is self-contained in
// namedstore.
func newSystemStore(
	drv driver.Driver,
	hashes domain.HashRegistry,
	cfg domain.StoreConfig,
	log *slog.Logger,
) *systemStore {
	return &systemStore{
		drv:    drv,
		hashes: hashes,
		cfg:    cfg,
		log:    log,
	}
}

// Put writes a SystemArtifact in the form its Keep selects (ADR-101).
// The payload is buffered (system payloads are small) and encoded as an
// inline manifest. Keep is optional: nil defaults to keep=1 (atomic
// versioned "latest", no history) — the safe default, so a forgotten
// Keep never silently produces the exclusive-cell (lock) form. keep=0
// (KeepCell) overwrites the exclusive cell in place (a lock's
// exclusive-acquire discipline is a caller-side policy over
// namedstore.WriteCell, not here); keep≥1 (KeepVersions) claims the
// next seq and prunes older versions best-effort.
func (ss *systemStore) Put(ctx context.Context, a SystemArtifact) error {
	if err := namedstore.ValidateName(a.Name); err != nil {
		return err
	}
	body, err := io.ReadAll(a.Payload)
	if err != nil {
		return fmt.Errorf("system store: read payload for %q: %w", a.Name, err)
	}
	fileBytes, _, err := namedstore.BuildInlineManifest(body, string(ss.cfg.ContentHasher), ss.hashes)
	if err != nil {
		return fmt.Errorf("system store: build %q: %w", a.Name, err)
	}

	// Keep selects the form. nil → the default (keep=1, versioned); the
	// exclusive cell is opt-in only, via KeepCell() (*Keep == 0).
	keep := defaultKeepVersions
	if a.Keep != nil {
		keep = int(*a.Keep)
	}

	if keep == 0 {
		// keep=0 — exclusive cell. Put overwrites in place (last-write
		// wins); a lock's create-if-absent acquire uses
		// namedstore.WriteCell(exclusive=true) directly.
		if err := namedstore.WriteCell(ctx, ss.drv, a.Name, fileBytes, false); err != nil {
			return fmt.Errorf("system store: put cell %q: %w", a.Name, err)
		}
		return nil
	}

	// keep≥1 — versioned: publish next seq, then prune to keep.
	if _, _, err := namedstore.ClaimVersion(ctx, ss.drv, a.Name, fileBytes); err != nil {
		return fmt.Errorf("system store: put %q: %w", a.Name, err)
	}
	// Retention is GC, not a liveness step: a prune failure leaves an
	// invisible old version for the next prune to reclaim and never
	// invalidates the version just written.
	if err := namedstore.Prune(ctx, ss.drv, a.Name, keep); err != nil {
		ss.logger().LogAttrs(ctx, slog.LevelWarn,
			"system artifact prune failed (old versions left for next prune)",
			slog.String("name", a.Name), slog.String("error", err.Error()))
	}
	return nil
}

// Get opens the active version (max seq) or, when the name has no
// versions, the keep=0 cell. A name is exactly one form, so at most one
// resolves. Returns errs.ErrArtifactNotFound when the name has never
// been written.
func (ss *systemStore) Get(ctx context.Context, name string) (domain.ReadHandle, error) {
	seq, found, err := namedstore.ResolveActiveSeq(ctx, ss.drv, name)
	if err != nil {
		return nil, fmt.Errorf("system store: get %q: %w", name, err)
	}
	if found {
		path, err := namedstore.VersionPath(name, seq)
		if err != nil {
			return nil, err
		}
		m, err := namedstore.Load(ctx, ss.drv, ss.hashes, path)
		if err != nil {
			return nil, fmt.Errorf("system store: get %q: %w", name, err)
		}
		return artifactio.NewInlineHandle(m), nil
	}
	// No versions — try the cell. LoadCell maps an absent cell to
	// errs.ErrArtifactNotFound, so this is also the not-found path.
	m, err := namedstore.LoadCell(ctx, ss.drv, ss.hashes, name)
	if err != nil {
		return nil, err
	}
	return artifactio.NewInlineHandle(m), nil
}

// Delete removes every version AND any cell of name. A name is one form,
// but removing both is form-agnostic and idempotent (each is a no-op
// when absent).
func (ss *systemStore) Delete(ctx context.Context, name string) error {
	if err := namedstore.RemoveAll(ctx, ss.drv, name); err != nil {
		return fmt.Errorf("system store: delete %q: %w", name, err)
	}
	if err := namedstore.RemoveCell(ctx, ss.drv, name); err != nil {
		return fmt.Errorf("system store: delete %q: %w", name, err)
	}
	return nil
}

// Walk iterates over every name with the given prefix in alphabetical
// order, yielding the active manifest for each — both versioned actives
// (ListActive) and keep=0 cells (ListCells, e.g. the lease). A name is
// one form, so the merged lists never overlap.
func (ss *systemStore) Walk(ctx context.Context, prefix string, cb func(name string, m domain.Manifest) error) error {
	versions, err := namedstore.ListActive(ctx, ss.drv, prefix)
	if err != nil {
		return err
	}
	cells, err := namedstore.ListCells(ctx, ss.drv, prefix)
	if err != nil {
		return err
	}
	all := append(versions, cells...)
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	for _, a := range all {
		m, err := namedstore.Load(ctx, ss.drv, ss.hashes, a.Path)
		if err != nil {
			// A version/cell that vanished or failed verification
			// mid-walk is skipped rather than aborting the rest.
			continue
		}
		if err := cb(a.Name, m); err != nil {
			return err
		}
	}
	return nil
}

// logger returns the systemStore's logger, never nil. Mirrors the
// store-level nil-safety so call sites need no guard.
func (ss *systemStore) logger() *slog.Logger {
	if ss.log == nil {
		return discardLogger
	}
	return ss.log
}
