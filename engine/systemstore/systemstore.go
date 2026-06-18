// Package systemstore is the facade for engine-internal service
// artifacts: versioned configuration, agent cursors, index snapshots, and
// the like, each addressed by a slash-separated name rather than by
// content hash. These artifacts live outside the content-addressed index,
// in their own address space, and are invisible to the data-plane Walk.
//
// It is reached through the store's AdminStore.System(); it shares no
// mutable state with the store core (only the driver, the hash registry,
// the immutable ContentHasher, and a logger), which is why it lives in
// its own package rather than as a facet over the store core.
//
// The on-disk layout (versioned names, keep=0 cells, exclusive-create
// publishing, verify-on-read) lives in engine/internal/namedartifact (ADR-85).
package systemstore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/namedartifact"
	"scrinium.dev/internal/slogx"
)

// Artifact is an engine-internal service artifact, addressed by a
// slash-separated Name rather than by content hash. Unlike a data-plane
// domain.Artifact it carries no Ext/Usr metadata — system payloads are
// small, opaque service blobs (config versions, agent cursors, index
// snapshots). The Name is the address: Put writes the payload as a new
// version of the name; Get reads the active version; Delete removes the
// name. Versioning, activation (max seq), exclusive-create publishing,
// and verify-on-read integrity live in engine/internal/namedartifact (ADR-85).
//
// Named addressing is a deliberately small facility for the engine's
// own data — not a general user-facing primitive — which is why it
// lives behind AdminStore.System() and uses its own type rather than
// overloading domain.Artifact.
type Artifact struct {
	// Name is the slash-separated name under which the artifact is
	// stored and later retrieved (e.g. "scrub/cursor").
	Name string

	// Payload is the artifact body. System payloads are small enough to
	// buffer in memory.
	Payload io.Reader

	// Keep selects the storage form (ADR-100/101). It is optional:
	//   nil             → the default, keep=1 (atomic versioned "latest",
	//                     no history). Forgetting Keep is safe — it never
	//                     yields the exclusive-cell (lock) form.
	//   *Keep == 0      → exclusive cell: one fixed slot (<name>), no
	//                     versions, overwrite in place (the keep=0 / lock
	//                     form). Opt-in only — build it with KeepCell().
	//   *Keep ∈ [1,255] → versions: <name>/<seq>, active = max(seq),
	//                     pruned to *Keep retained. Build with KeepVersions(n).
	Keep *uint8
}

// KeepCell marks an Artifact as a keep=0 exclusive cell: a single
// fixed slot, overwritten in place (ADR-100/101). The lock form.
func KeepCell() *uint8 { var k uint8; return &k }

// KeepVersions marks an Artifact as keep=n versioned storage
// (<name>/<seq>, active = max, pruned to n retained). n must be ≥ 1; n=0
// is the cell form — use KeepCell for that.
func KeepVersions(n uint8) *uint8 { return &n }

// Store is the facade for engine-internal service artifacts: versioned
// configuration, agent cursors, index snapshots, and the like, each
// addressed by a slash-separated name. Artifacts are stored outside the
// content-addressed index, in their own address space, and are invisible
// to the data-plane Walk.
type Store interface {
	// Put writes an Artifact in the form its Keep selects (ADR-101):
	// keep=0 overwrites the exclusive cell in place; keep≥1 publishes a
	// new version (active = max seq) and prunes to Keep retained. Keep is
	// optional — nil defaults to keep=1 (versioned latest, no history).
	Put(ctx context.Context, a Artifact) error

	// Get opens the active version (max seq) or, for a keep=0 name, the
	// cell. Returns errs.ErrArtifactNotFound when the name has never been
	// written.
	Get(ctx context.Context, name string) (domain.ReadHandle, error)

	// Delete removes every version AND any cell of name. Idempotent:
	// deleting an absent name returns nil.
	Delete(ctx context.Context, name string) error

	// Walk iterates over every name with the given prefix in
	// alphabetical order, yielding the active manifest for each — both
	// versioned actives and keep=0 cells (e.g. the lease).
	Walk(ctx context.Context, prefix string, cb func(name string, m domain.Manifest) error) error
}

// systemStore is the Store facade over the pointer-free layout (ADR-85,
// engine/internal/namedartifact). Every system name maps to system/<name>/<seq>; the
// active version is max(seq); a write claims the next seq with an
// exclusive create. System artifacts are never indexed in StoreIndex and
// never written under manifests/ — they live in their own address space,
// so they are invisible to the data-plane Walk (handle-IS-NULL) by
// construction rather than by an index filter.
type systemStore struct {
	drv    driver.Driver
	hashes domain.HashRegistry
	cfg    domain.StoreConfig // immutable fields only (ContentHasher); see New
	log    *slog.Logger
}

// defaultKeepVersions is the form an Artifact takes when Keep is nil
// (unset): keep=1 — atomic, pointerless "latest" with no retained
// history. The safe default — forgetting Keep yields working versioned
// storage, never the exclusive-cell (lock) form, which requires an
// explicit KeepCell(). History (keep>1) is opt-in via KeepVersions(n);
// the config writer keeps its own history through namedartifact directly.
const defaultKeepVersions = 1

// Compile-time check that the concrete type satisfies the contract.
var _ Store = (*systemStore)(nil)

// New wires the facade. It needs only the driver (the layout is on-disk),
// the hash registry (verify-on-read and the content hash of each write),
// the active config (for its immutable ContentHasher), and a logger for
// best-effort prune failures. No StoreIndex and no write indirection: the
// inline-manifest write is self-contained in namedartifact.
func New(
	drv driver.Driver,
	hashes domain.HashRegistry,
	cfg domain.StoreConfig,
	log *slog.Logger,
) Store {
	return &systemStore{
		drv:    drv,
		hashes: hashes,
		cfg:    cfg,
		log:    log,
	}
}

// Put writes an Artifact in the form its Keep selects (ADR-101). The
// payload is buffered (system payloads are small) and encoded as an
// inline manifest. Keep is optional: nil defaults to keep=1 (atomic
// versioned "latest", no history) — the safe default, so a forgotten
// Keep never silently produces the exclusive-cell (lock) form. keep=0
// (KeepCell) overwrites the exclusive cell in place (a lock's
// exclusive-acquire discipline is a caller-side policy over
// namedartifact.WriteCell, not here); keep≥1 (KeepVersions) claims the
// next seq and prunes older versions best-effort.
func (ss *systemStore) Put(ctx context.Context, a Artifact) error {
	if err := namedartifact.ValidateName(a.Name); err != nil {
		return err
	}
	body, err := io.ReadAll(a.Payload)
	if err != nil {
		return fmt.Errorf("system store: read payload for %q: %w", a.Name, err)
	}
	fileBytes, _, err := namedartifact.BuildInlineManifest(body, string(ss.cfg.ContentHasher), ss.hashes)
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
		// namedartifact.WriteCell(exclusive=true) directly.
		if err := namedartifact.WriteCell(ctx, ss.drv, a.Name, fileBytes, false); err != nil {
			return fmt.Errorf("system store: put cell %q: %w", a.Name, err)
		}
		return nil
	}

	// keep≥1 — versioned: publish next seq, then prune to keep.
	if _, _, err := namedartifact.ClaimVersion(ctx, ss.drv, a.Name, fileBytes); err != nil {
		return fmt.Errorf("system store: put %q: %w", a.Name, err)
	}
	// Retention is GC, not a liveness step: a prune failure leaves an
	// invisible old version for the next prune to reclaim and never
	// invalidates the version just written.
	if err := namedartifact.Prune(ctx, ss.drv, a.Name, keep); err != nil {
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
	seq, found, err := namedartifact.ResolveActiveSeq(ctx, ss.drv, name)
	if err != nil {
		return nil, fmt.Errorf("system store: get %q: %w", name, err)
	}
	if found {
		path, err := namedartifact.VersionPath(name, seq)
		if err != nil {
			return nil, err
		}
		m, err := namedartifact.Load(ctx, ss.drv, ss.hashes, path)
		if err != nil {
			return nil, fmt.Errorf("system store: get %q: %w", name, err)
		}
		return artifact.NewInlineHandle(m), nil
	}
	// No versions — try the cell. LoadCell maps an absent cell to
	// errs.ErrArtifactNotFound, so this is also the not-found path.
	m, err := namedartifact.LoadCell(ctx, ss.drv, ss.hashes, name)
	if err != nil {
		return nil, err
	}
	return artifact.NewInlineHandle(m), nil
}

// Delete removes every version AND any cell of name. A name is one form,
// but removing both is form-agnostic and idempotent (each is a no-op
// when absent).
func (ss *systemStore) Delete(ctx context.Context, name string) error {
	if err := namedartifact.RemoveAll(ctx, ss.drv, name); err != nil {
		return fmt.Errorf("system store: delete %q: %w", name, err)
	}
	if err := namedartifact.RemoveCell(ctx, ss.drv, name); err != nil {
		return fmt.Errorf("system store: delete %q: %w", name, err)
	}
	return nil
}

// Walk iterates over every name with the given prefix in alphabetical
// order, yielding the active manifest for each — both versioned actives
// (ListActive) and keep=0 cells (ListCells, e.g. the lease). A name is
// one form, so the merged lists never overlap.
func (ss *systemStore) Walk(ctx context.Context, prefix string, cb func(name string, m domain.Manifest) error) error {
	versions, err := namedartifact.ListActive(ctx, ss.drv, prefix)
	if err != nil {
		return err
	}
	cells, err := namedartifact.ListCells(ctx, ss.drv, prefix)
	if err != nil {
		return err
	}
	all := append(versions, cells...)
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	for _, a := range all {
		m, err := namedartifact.Load(ctx, ss.drv, ss.hashes, a.Path)
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
	return slogx.OrDiscard(ss.log)
}
