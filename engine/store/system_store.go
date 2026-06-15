package store

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/store/internal/artifactio"
	"scrinium.dev/engine/store/internal/systemlayout"
	"scrinium.dev/errs"
)

// systemStore is the SystemStore facade over the pointer-free layout
// (ADR-85, engine/store/internal/systemlayout). Every system name maps
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

	// keep is the number of historical versions retained per name after
	// a write. The active version is always retained; older versions are
	// best-effort GC.
	keep int
}

// systemKeepVersions is the default retained-version count for a system
// name: the active version plus a small history for rollback/diagnosis.
const systemKeepVersions = 3

// Compile-time check that the concrete type satisfies the contract.
var _ SystemStore = (*systemStore)(nil)

// newSystemStore wires the facade. It needs only the driver (the layout
// is on-disk), the hash registry (verify-on-read and the content hash of
// each write), the active config (for its immutable ContentHasher), and
// a logger for best-effort prune failures. No StoreIndex and no write
// indirection: the inline-manifest write is self-contained in
// systemlayout.
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
		keep:   systemKeepVersions,
	}
}

// Put writes a SystemArtifact as a new version of its Name. The payload
// is buffered (system payloads are small), encoded as an inline
// manifest, and published by claiming the next seq. After a successful
// write, versions older than keep are pruned best-effort.
func (ss *systemStore) Put(ctx context.Context, a SystemArtifact) error {
	if err := systemlayout.ValidateName(a.Name); err != nil {
		return err
	}
	body, err := io.ReadAll(a.Payload)
	if err != nil {
		return fmt.Errorf("system store: read payload for %q: %w", a.Name, err)
	}
	fileBytes, _, err := systemlayout.BuildInlineManifest(body, string(ss.cfg.ContentHasher), ss.hashes)
	if err != nil {
		return fmt.Errorf("system store: build %q: %w", a.Name, err)
	}
	if _, _, err := systemlayout.ClaimVersion(ctx, ss.drv, a.Name, fileBytes); err != nil {
		return fmt.Errorf("system store: put %q: %w", a.Name, err)
	}
	// Retention is GC, not a liveness step: a prune failure leaves an
	// invisible old version for the next prune to reclaim and never
	// invalidates the version just written.
	if err := systemlayout.Prune(ctx, ss.drv, a.Name, ss.keep); err != nil {
		ss.logger().LogAttrs(ctx, slog.LevelWarn,
			"system artifact prune failed (old versions left for next prune)",
			slog.String("name", a.Name), slog.String("error", err.Error()))
	}
	return nil
}

// Get opens the active version of name. Returns errs.ErrArtifactNotFound
// when the name has never been written.
func (ss *systemStore) Get(ctx context.Context, name string) (domain.ReadHandle, error) {
	seq, found, err := systemlayout.ResolveActiveSeq(ctx, ss.drv, name)
	if err != nil {
		return nil, fmt.Errorf("system store: get %q: %w", name, err)
	}
	if !found {
		return nil, errs.ErrArtifactNotFound
	}
	path, err := systemlayout.VersionPath(name, seq)
	if err != nil {
		return nil, err
	}
	m, err := systemlayout.Load(ctx, ss.drv, ss.hashes, path)
	if err != nil {
		return nil, fmt.Errorf("system store: get %q: %w", name, err)
	}
	return artifactio.NewInlineHandle(m), nil
}

// Delete removes every version of name. Idempotent: deleting an absent
// name returns nil.
func (ss *systemStore) Delete(ctx context.Context, name string) error {
	if err := systemlayout.RemoveAll(ctx, ss.drv, name); err != nil {
		return fmt.Errorf("system store: delete %q: %w", name, err)
	}
	return nil
}

// Walk iterates over every name with the given prefix in alphabetical
// order, yielding the active manifest for each.
func (ss *systemStore) Walk(ctx context.Context, prefix string, cb func(name string, m domain.Manifest) error) error {
	actives, err := systemlayout.ListActive(ctx, ss.drv, prefix)
	if err != nil {
		return err
	}
	for _, a := range actives {
		m, err := systemlayout.Load(ctx, ss.drv, ss.hashes, a.Path)
		if err != nil {
			// A version that vanished or failed verification mid-walk is
			// skipped rather than aborting the enumeration of the rest.
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
