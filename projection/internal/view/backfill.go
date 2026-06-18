package view

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/projection/internal/source"
)

// --- Construction ---

// New constructs a View by walking source and populating
// every tree. Backfill is synchronous: New returns only after
// the source has been fully traversed.
//
// Default options:
//   - root view: the first available root (intrinsic or provided)
//   - fallback: FallbackOrphaned
//   - filter: empty
//
// EventBus is optional via WithEventBus; without it the View
// silently produces no events.
func New(ctx context.Context, src source.Provider, opts ...Option) (*View, error) {
	if src == nil {
		return nil, fmt.Errorf("projection.New: source is nil")
	}

	o := viewOptions{
		fallback: FallbackOrphaned,
	}
	for _, opt := range opts {
		opt(&o)
	}

	v := &View{
		Source:    source.KindStore,
		CreatedAt: time.Now().UTC(),

		src:  src,
		bus:  o.bus,
		opts: o,

		trees:      make(map[RootView]map[string]*viewNode),
		artifacts:  make(map[domain.ArtifactID]*artifactRecord),
		pathOwner:  make(map[RootView]map[string]domain.ArtifactID),
		pathLosers: make(map[RootView]map[string][]loserEntry),
		collide:    make(map[RootView]bool),
		seenKeys:   make(map[RootView]map[string]struct{}),
	}

	// Build the active view set (intrinsic core + extension-provided)
	// and initialise per-root state.
	v.defs = v.buildViewDefs()
	roots := map[RootView]bool{RootByOrphaned: true}
	for _, d := range v.defs {
		roots[d.root] = true
		if d.collide {
			v.collide[d.root] = true
			v.pathOwner[d.root] = make(map[string]domain.ArtifactID)
			v.pathLosers[d.root] = make(map[string][]loserEntry)
		}
		if d.countKey != nil {
			v.seenKeys[d.root] = make(map[string]struct{})
		}
	}

	// Resolve the root view. The client picks it by name (via config);
	// when unset we default to the first available root, and a named
	// root that does not exist is an error. The projection itself names
	// none of the roots.
	if v.opts.rootView == "" {
		rs := make([]RootView, 0, len(roots))
		for r := range roots {
			rs = append(rs, r)
		}
		sort.Slice(rs, func(i, j int) bool { return rs[i] < rs[j] })
		if len(rs) > 0 {
			v.opts.rootView = rs[0]
		}
	} else if !roots[v.opts.rootView] {
		return nil, fmt.Errorf("projection.New: root view %q is not available", v.opts.rootView)
	}

	// Initialise tree roots so List on an empty tree returns
	// children rather than ErrPathNotFound.
	for r := range roots {
		v.trees[r] = map[string]*viewNode{"": newDirNode("", "", v.CreatedAt)}
	}

	startedAt := time.Now()
	if err := v.backfill(ctx); err != nil {
		return nil, err
	}
	v.publish(event.EventViewRebuilt, event.RebuiltPayload{
		Duration:  time.Since(startedAt),
		NodeCount: v.Stats.TotalNodes,
	})

	return v, nil
}

// buildViewDefs assembles the active view set: the intrinsic core
// definitions (derived from core manifest fields) augmented by the views
// active extensions provide via WithProvidedViews (ADR-98). The View is
// agnostic to what each provided view computes — by-path (fspath) and
// by-namespace (the namespace extension) arrive through the provided rail
// like any other, each carrying its own layout, collision and count policy.
func (v *View) buildViewDefs() []viewDef {
	defs := []viewDef{
		{root: RootByArtifact, path: pathByArtifactDef},
		{root: RootByDate, path: pathByDateDef},
		{root: RootBySession, path: pathBySessionDef, countKey: sessionCountKey},
	}
	for _, p := range v.opts.provided {
		defs = append(defs, viewDef{
			root:     p.Root,
			path:     p.Path,
			collide:  p.Collide,
			orphans:  p.Orphans,
			countKey: p.CountKey,
		})
	}
	return defs
}

// Intrinsic def path/count functions. These derive placement purely from
// core manifest fields, so they carry no extension knowledge.

func pathByArtifactDef(m domain.Manifest) (string, bool) { return byArtifactPath(m.ArtifactID), true }

func pathByDateDef(m domain.Manifest) (string, bool) { return byDatePath(m), true }

func pathBySessionDef(m domain.Manifest) (string, bool) {
	if m.SessionID == "" {
		return "", false
	}
	return bySessionPath(m), true
}

func sessionCountKey(m domain.Manifest) (string, bool) {
	if m.SessionID == "" {
		return "", false
	}
	return string(m.SessionID), true
}

// backfill walks the source and classifies each manifest. The
// caller holds no lock; backfill is single-threaded by virtue of
// running before New returns.
//
// Two paths exist:
//
//   - Fast path (an MetadataSource is configured). Source.Walk
//     gives us the stripped manifests; we top them up by
//     calling metadataSource.Metadata(id) — an O(log N) lookup against
//     a local index custom index that persisted the ext block at
//     write time. No round-trip to Source.Get per manifest.
//
//   - Slow path (no MetadataSource). We round-trip Source.Get for
//     every manifest to recover Ext. This is N+1 by
//     construction; acceptable for tests with FakeSource
//     (which keeps full manifests in memory anyway, so Get is
//     cheap) and for backfills small enough that latency
//     doesn't matter. Daemons with large stores configure
//     WithMetadataSource (or WithFSPathIndex) to take the fast path.
func (v *View) backfill(ctx context.Context) error {
	cb := func(m domain.Manifest) error {
		if !v.passesFilter(m) {
			return nil
		}
		v.populateExt(ctx, &m)
		v.indexArtifact(m, true /*duringBackfill*/)
		return nil
	}
	if err := v.src.Walk(ctx, cb); err != nil {
		return fmt.Errorf("%w: %w", errs.ErrSourceUnavailable, err)
	}
	return nil
}

// populateExt fills m.Ext (and a couple of cheap neighbouring
// fields) from the configured fast path or, failing that, from
// Source.Get. Mutates m in place.
//
// Errors from either path are intentionally swallowed — backfill
// must finish even if one manifest's ext fetch fails (the
// resolver will then treat it as orphaned, the standard
// missing-path behaviour). A noisy "best effort" log line could
// be added behind the bus once we wire it.
func (v *View) populateExt(ctx context.Context, m *domain.Manifest) {
	// Fast path: bulk MetadataSource lookup, no Source round-trip.
	if v.opts.metadataSource != nil {
		raw, ok, err := v.opts.metadataSource.Metadata(m.ArtifactID)
		if err == nil && ok && len(raw) > 0 {
			m.Ext = raw
			return
		}
		// Miss or error — fall through to Source.Get. A miss is
		// legitimate (artifact written before the custom index was
		// registered, or custom index doesn't index this schema);
		// for those rare cases the slow path is correct.
	}

	// Slow path: round-trip Source.Get for the full manifest.
	// Walk-style sources (store.Store-backed) usually return a
	// stripped manifest from the index — Ext, layout, inline
	// blob, etc. are absent. Resolvers like vfsmeta need Ext to
	// produce a path.
	//
	// FakeSource and similar in-memory test stubs already return
	// complete manifests; doing a Get on top is a cheap no-op.
	rh, err := v.src.Get(ctx, m.ArtifactID)
	if err != nil {
		return
	}
	full := rh.Manifest()
	rh.Close()
	// Prefer the canonical Ext block; fall back to the legacy
	// Metadata field for Sealed/Paranoid manifests whose crypto
	// path has not migrated yet.
	if extBytes := full.Ext; len(extBytes) > 0 {
		m.Ext = extBytes
	}
	if full.ContentHash != "" {
		m.ContentHash = full.ContentHash
	}
	if full.OriginalSize != 0 {
		m.OriginalSize = full.OriginalSize
	}
}

// passesFilter checks the configured Filter against a
// manifest. All non-zero conditions combine by AND. Prefix is
// applied to the resolver path — see resolvePathForManifest below.
func (v *View) passesFilter(m domain.Manifest) bool {
	f := v.opts.filter
	if f.SessionID != "" && m.SessionID != f.SessionID {
		return false
	}
	if f.Prefix != "" {
		path, ok := v.resolvePathForManifest(m)
		if !ok {
			return false
		}
		if !strings.HasPrefix(path, f.Prefix) {
			return false
		}
	}
	return true
}

// resolvePathForManifest applies the orphaning view's placement
// function, then the fallback policy, returning (path, true) when the
// artifact has any such representation (real or synthetic), or
// ("", false) when it does not (FallbackOrphaned + no placement). Used
// only for Filter.Prefix; the orphaning view is found in the active set
// so the View names none.
func (v *View) resolvePathForManifest(m domain.Manifest) (string, bool) {
	for _, d := range v.defs {
		if d.orphans && d.path != nil {
			if path, ok := d.path(m); ok {
				return path, true
			}
			break
		}
	}
	if v.opts.fallback == FallbackSynthetic {
		return v.syntheticPath(m), true
	}
	return "", false
}

// syntheticPath builds a synthetic by-path entry for artifacts
// without a resolver path. Format mirrors docs/4 §14.4.4:
//
//	<sid-shard>/<id-short>.bin   — session present
//	_anonymous/<id-short>.bin    — no session
func (v *View) syntheticPath(m domain.Manifest) string {
	idShort := shortID(m.ArtifactID)
	if m.SessionID != "" {
		return sessionShard(m.SessionID) + "/" + idShort + ".bin"
	}
	return "_anonymous/" + idShort + ".bin"
}

// indexArtifact registers an artifact in every applicable tree by
// iterating the active view set. duringBackfill is retained for symmetry
// (Stats are maintained identically on Add and backfill).
//
// The function does NOT take the View lock — callers (backfill,
// Add) handle locking themselves. backfill runs single-threaded;
// Add takes the write lock around the call.
func (v *View) indexArtifact(m domain.Manifest, duringBackfill bool) {
	rec := &artifactRecord{manifest: m, paths: make(map[RootView]string)}

	for _, d := range v.defs {
		var (
			path string
			ok   bool
		)
		if d.path != nil {
			path, ok = d.path(m)
		}
		if !ok {
			// No placement in this view. Orphaning views (by-path)
			// send the artifact to the orphan tree, or to a synthetic
			// by-path entry under FallbackSynthetic; other views skip.
			if !d.orphans {
				continue
			}
			if v.opts.fallback == FallbackSynthetic {
				sp := v.syntheticPath(m)
				rec.paths[d.root] = sp
				if d.collide {
					v.applyCollisionInsert(d.root, sp, m, rec)
				} else {
					v.insertFile(v.trees[d.root], sp, m)
				}
			} else {
				op := byArtifactPath(m.ArtifactID)
				rec.paths[RootByOrphaned] = op
				v.insertFile(v.trees[RootByOrphaned], op, m)
				v.Stats.OrphanedCount++
			}
			continue
		}

		rec.paths[d.root] = path
		if d.collide {
			v.applyCollisionInsert(d.root, path, m, rec)
		} else {
			v.insertFile(v.trees[d.root], path, m)
		}
		if d.countKey != nil {
			if key, kok := d.countKey(m); kok {
				v.seenKeys[d.root][key] = struct{}{}
			}
		}
	}

	v.artifacts[m.ArtifactID] = rec

	// Stats. TotalNodes counts artifacts (not virtual dirs).
	v.Stats.TotalNodes++
	v.Stats.TotalBytes += m.OriginalSize
	// Distinct-cardinality counters from the per-view seen-key sets.
	// by-session is an intrinsic view and keeps a named counter; every
	// other counting view (including any extension-provided one) lands
	// in ViewCounts under its own root, so this stays generic.
	v.Stats.SessionCount = int64(len(v.seenKeys[RootBySession]))
	for root, keys := range v.seenKeys {
		if root == RootBySession {
			continue
		}
		if v.Stats.ViewCounts == nil {
			v.Stats.ViewCounts = make(map[RootView]int64)
		}
		v.Stats.ViewCounts[root] = int64(len(keys))
	}
	_ = duringBackfill
}

// applyCollisionInsert handles arbitration for a collidable tree
// (path keys not artifact-unique, e.g. by-path), keyed by root. Three
// cases:
//
//  1. path is unclaimed → insert as winner.
//  2. path is claimed; newcomer is fresher → newcomer wins, previous
//     owner becomes loser, event.EventPathCollision emitted.
//  3. path is claimed; newcomer is older → newcomer joins the losers
//     list, no node, event.EventPathCollision emitted.
//
// The caller records rec.paths[root] before calling (a loser still has
// its path recorded for Remove). The "fresher" rule is CreatedAt
// descending; on tie the lexicographically larger ArtifactID wins.
func (v *View) applyCollisionInsert(root RootView, path string, m domain.Manifest, rec *artifactRecord) {
	owners := v.pathOwner[root]
	tree := v.trees[root]

	currentOwner, claimed := owners[path]
	if !claimed {
		owners[path] = m.ArtifactID
		v.insertFile(tree, path, m)
		return
	}

	currentRec := v.artifacts[currentOwner]
	if currentRec == nil {
		// Should not happen: owner without artifact record.
		// Recover by treating as unclaimed.
		owners[path] = m.ArtifactID
		v.insertFile(tree, path, m)
		return
	}

	if isFresherWinner(m, currentRec.manifest) {
		// Newcomer wins. Demote previous owner.
		owners[path] = m.ArtifactID
		v.removeFile(tree, path)
		v.insertFile(tree, path, m)
		v.pushLoser(root, path, currentRec.manifest)
		v.publish(event.EventPathCollision, event.PathCollisionPayload{
			Path:   path,
			Winner: m.ArtifactID,
			Loser:  currentOwner,
		})
		v.Stats.CollisionCount++
		return
	}

	// Newcomer loses.
	v.pushLoser(root, path, m)
	v.publish(event.EventPathCollision, event.PathCollisionPayload{
		Path:   path,
		Winner: currentOwner,
		Loser:  m.ArtifactID,
	})
	v.Stats.CollisionCount++
}

// isFresherWinner reports whether candidate beats incumbent for
// the by-path slot. CreatedAt later wins; on tie lexicographically
// larger ArtifactID wins.
func isFresherWinner(candidate, incumbent domain.Manifest) bool {
	if candidate.CreatedAt.After(incumbent.CreatedAt) {
		return true
	}
	if candidate.CreatedAt.Equal(incumbent.CreatedAt) {
		return string(candidate.ArtifactID) > string(incumbent.ArtifactID)
	}
	return false
}

// pushLoser inserts an entry into pathLosers[root][path], keeping the
// slice sorted by CreatedAt descending (and ArtifactID descending on
// tie). The inner slice is allocated lazily.
func (v *View) pushLoser(root RootView, path string, m domain.Manifest) {
	byPath := v.pathLosers[root]
	losers := byPath[path]
	entry := loserEntry{id: m.ArtifactID, createdAt: m.CreatedAt}
	idx := sort.Search(len(losers), func(i int) bool {
		// sort descending: we want the position of the first entry
		// "older or equal" to the new one. That position is the
		// insertion point.
		l := losers[i]
		if l.createdAt.After(entry.createdAt) {
			return false
		}
		if l.createdAt.Equal(entry.createdAt) {
			return string(l.id) <= string(entry.id)
		}
		return true
	})
	losers = append(losers, loserEntry{})
	copy(losers[idx+1:], losers[idx:])
	losers[idx] = entry
	byPath[path] = losers
}

// removeLoser drops the entry with the given id from
// pathLosers[root][path]; no-op if not present.
func (v *View) removeLoser(root RootView, path string, id domain.ArtifactID) {
	byPath := v.pathLosers[root]
	losers := byPath[path]
	for i, l := range losers {
		if l.id == id {
			byPath[path] = append(losers[:i], losers[i+1:]...)
			if len(byPath[path]) == 0 {
				delete(byPath, path)
			}
			return
		}
	}
}
