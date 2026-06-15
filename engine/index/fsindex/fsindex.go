package fsindex

import (
	"context"
	"encoding/json"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/domain/fsmeta"
	"scrinium.dev/engine/index/extension"
)

// Tables under the extension namespace. Two K/V groups:
//
//   - byID:   artifactID → fsmeta JSON (primary, source of truth)
//   - byPath: "<path>\x00<artifactID>" → "" (reverse, supports
//     LookupByPath and prefix scans through path-space)
//
// The trailing "\x00<artifactID>" suffix in byPath keys lets two
// artifacts share a path (rare but legal — e.g. transient
// duplicates during reindex) without colliding on the same key.
// Lookups by path do a prefix scan and pick the first hit.
const (
	tableByID   = "byID"
	tableByPath = "byPath"
)

// Name is the stable extension identifier.
const Name = "scrinium.fsindex"

// schemaVersion is the on-disk layout version. Bump and add a
// migration switch in Setup whenever the table layout changes.
const schemaVersion = 1

// Extension is the fsmeta-aware projection of artifact metadata.
// Implements extension.CustomIndex. Construct via New, register
// via *sqlite.Index.Extensions().Register.
type Extension struct {
	// store is captured during Setup and used by the read-side
	// API (GetByID, LookupByPath, WalkAll) for the lifetime of
	// the StoreIndex. Backend swaps the underlying executor
	// from tx-mode to db-mode atomically after Register commits;
	// the captured reference stays valid throughout.
	store extension.ExtensionStore
}

// New returns a fresh Extension. The instance is not registered
// — caller passes it to *sqlite.Index.Extensions().Register(ctx, ext).
func New() *Extension {
	return &Extension{}
}

// Name returns the stable identifier.
func (e *Extension) Name() string { return Name }

// SchemaVersion returns the current data layout version.
func (e *Extension) SchemaVersion() int { return schemaVersion }

// Subscribe declares the index events fsindex reacts to.
// ManifestIndexed: stash fsmeta; ManifestDeleted: drop it.
func (e *Extension) Subscribe() []extension.EventKind {
	return []extension.EventKind{
		extension.EventKindManifestIndexed,
		extension.EventKindManifestDeleted,
	}
}

// Setup runs once per registration. v1 is initial; nothing to
// migrate on a fresh DB or on an existing v1.
func (e *Extension) Setup(ctx context.Context, store extension.ExtensionStore, oldVersion int) error {
	switch oldVersion {
	case 0, 1:
		// Nothing to do — schema is implicit (the K/V tables
		// exist by virtue of ext_data being created at
		// migration v2 in the sqlite backend).
	default:
		return fmt.Errorf("fsindex: unsupported old schema version: %d", oldVersion)
	}
	e.store = store
	return nil
}

// Apply is invoked by the index inside the surrounding write
// transaction. ManifestIndexed and ManifestDeleted are the only
// kinds we subscribe to; anything else is a backend bug.
func (e *Extension) Apply(ctx context.Context, store extension.ExtensionStore, kind extension.EventKind, args extension.EventArgs) error {
	switch kind {
	case extension.EventKindManifestIndexed:
		return e.applyIndexed(store, args)
	case extension.EventKindManifestDeleted:
		return e.applyDeleted(store, args)
	default:
		return fmt.Errorf("fsindex: unexpected event kind %s", kind)
	}
}

// applyIndexed stores the fsmeta payload (bytes verbatim) plus a
// reverse-index entry for path lookup. Manifests that don't
// carry a fsmeta payload (foreign schema, system artifacts) are
// silently skipped — the extension only indexes what it
// understands.
func (e *Extension) applyIndexed(store extension.ExtensionStore, args extension.EventArgs) error {
	fs, ok, err := fsmeta.Decode(args.Manifest.Ext)
	if err != nil {
		// Decode errors mean the ext block claims to be fsmeta
		// (right marker) but is structurally broken. We log via
		// returning — the surrounding tx will roll back. Strict
		// mode is the contract per ADR-49.
		return fmt.Errorf("fsindex: decode ext for %q: %w",
			args.ArtifactID, err)
	}
	if !ok {
		// Foreign schema or no fsmeta — not our concern.
		return nil
	}

	id := string(args.ArtifactID)

	// Forward: id → raw fsmeta JSON (the bytes the manifest
	// actually carries; we don't re-encode `fs` because that
	// would lose forward-compatibility with future fsmeta
	// versions that add fields fsindex doesn't understand).
	if err := store.Put(tableByID, id, []byte(args.Manifest.Ext)); err != nil {
		return fmt.Errorf("fsindex: put byID: %w", err)
	}

	// Reverse: <path>\x00<id> → id. The suffix disambiguates
	// duplicate paths; LookupByPath returns the first hit in
	// lexicographic order (stable across runs since both keys
	// are deterministic).
	rkey := fs.Path + "\x00" + id
	if err := store.Put(tableByPath, rkey, []byte(id)); err != nil {
		return fmt.Errorf("fsindex: put byPath: %w", err)
	}
	return nil
}

// applyDeleted removes both forward and reverse entries. We need
// the path to delete the reverse key, so we read the stored
// fsmeta first. If the artifact wasn't indexed (no fsmeta on
// write) the byID Get returns ok=false and we exit cleanly.
func (e *Extension) applyDeleted(store extension.ExtensionStore, args extension.EventArgs) error {
	id := string(args.ArtifactID)

	raw, ok, err := store.Get(tableByID, id)
	if err != nil {
		return fmt.Errorf("fsindex: get byID for delete: %w", err)
	}
	if !ok {
		// Not indexed. Nothing to do.
		return nil
	}

	// Decode the stored payload to recover the path. We don't
	// trust args.Manifest.Ext here — for deletion the
	// backend passes a zero Manifest.
	fs, ok, err := fsmeta.Decode(raw)
	if err != nil {
		// Persisted bytes are corrupted. Forward is gone after
		// Delete, but we can't undo the orphaned reverse entry —
		// best effort: drop the forward and let an offline
		// reconciler clean up later.
		if delErr := store.Delete(tableByID, id); delErr != nil {
			return fmt.Errorf("fsindex: delete byID after decode failure: %w", delErr)
		}
		return fmt.Errorf("fsindex: decode persisted fsmeta for %q: %w", id, err)
	}
	if !ok {
		// Persisted but not fsmeta-shaped — shouldn't happen
		// because we only Put when fsmeta.Decode says ok.
		// Treat defensively as "no reverse key to clean".
		return store.Delete(tableByID, id)
	}

	if err := store.Delete(tableByID, id); err != nil {
		return fmt.Errorf("fsindex: delete byID: %w", err)
	}
	rkey := fs.Path + "\x00" + id
	if err := store.Delete(tableByPath, rkey); err != nil {
		return fmt.Errorf("fsindex: delete byPath: %w", err)
	}
	return nil
}

// Close releases extension-side resources. Backend storage stays
// owned by the StoreIndex.
func (e *Extension) Close() error {
	e.store = nil
	return nil
}

// --- Read API ---

// GetByID returns the persisted fsmeta JSON for the given
// artifact id, or (nil, false, nil) if not indexed.
//
// Returns the bytes verbatim — callers decode with fsmeta.Decode
// (or any future schema decoder) themselves. The View backfill
// fast-path uses this to materialise FilesystemFacet without
// round-tripping to Source.Get.
func (e *Extension) GetByID(id domain.ArtifactID) (json.RawMessage, bool, error) {
	if e.store == nil {
		return nil, false, fmt.Errorf("fsindex: not registered")
	}
	value, ok, err := e.store.Get(tableByID, string(id))
	if err != nil {
		return nil, false, fmt.Errorf("fsindex: GetByID: %w", err)
	}
	if !ok {
		return nil, false, nil
	}
	return json.RawMessage(value), true, nil
}

// Ext implements source.Ext (declared in the
// projection package). Same shape as GetByID — separate method
// kept so projection can reference an interface without taking
// a concrete dependency on fsindex.
func (e *Extension) Ext(id domain.ArtifactID) (json.RawMessage, bool, error) {
	return e.GetByID(id)
}

// LookupByPath returns the first ArtifactID whose fsmeta path
// equals `path`. Lexicographic tiebreak when more than one
// artifact shares the path (rare; usually a transient).
//
// Returns "" + (false, nil) if no artifact is indexed at this
// path — distinct from infrastructure errors which surface as
// the third return.
func (e *Extension) LookupByPath(path string) (domain.ArtifactID, bool, error) {
	if e.store == nil {
		return "", false, fmt.Errorf("fsindex: not registered")
	}

	prefix := path + "\x00"
	var found domain.ArtifactID
	err := e.store.Scan(tableByPath, prefix, func(_ string, value []byte) error {
		found = domain.ArtifactID(value)
		return extension.ErrStopScan
	})
	if err != nil {
		return "", false, fmt.Errorf("fsindex: LookupByPath: %w", err)
	}
	if found == "" {
		return "", false, nil
	}
	return found, true, nil
}

// WalkAll iterates every (artifactID, fsmeta JSON) pair in
// lexicographic id order. Returning fs.SkipAll from cb (the
// extensions sentinel extension.ErrStopScan also works) ends the
// walk cleanly.
//
// Used by projection.View.backfill: one round-trip yields every
// node's fs metadata.
func (e *Extension) WalkAll(cb func(id domain.ArtifactID, raw json.RawMessage) error) error {
	if e.store == nil {
		return fmt.Errorf("fsindex: not registered")
	}
	return e.store.Scan(tableByID, "", func(key string, value []byte) error {
		return cb(domain.ArtifactID(key), json.RawMessage(value))
	})
}

// Compile-time conformance: Extension satisfies
// extension.CustomIndex. Catches signature drift early.
var _ extension.CustomIndex = (*Extension)(nil)
