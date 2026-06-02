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
//   - root view: RootByPath (informational only)
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
		rootView: RootByPath,
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

		byPath:      make(map[string]*viewNode),
		bySession:   make(map[string]*viewNode),
		byNamespace: make(map[string]*viewNode),
		byDate:      make(map[string]*viewNode),
		byArtifact:  make(map[string]*viewNode),
		byOrphaned:  make(map[string]*viewNode),

		artifacts:  make(map[domain.ArtifactID]*artifactRecord),
		pathOwner:  make(map[string]domain.ArtifactID),
		pathLosers: make(map[string][]loserEntry),

		seenSessions:   make(map[domain.SessionID]struct{}),
		seenNamespaces: make(map[string]struct{}),
	}

	// Initialise tree roots so List on empty tree returns
	// children rather than ErrPathNotFound.
	for _, tree := range v.allTrees() {
		tree[""] = newDirNode("", "", v.CreatedAt)
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

// allTrees returns every tree pointer in a stable order. Used
// for whole-set initialisation; per-tree access goes through the
// named fields directly.
func (v *View) allTrees() []map[string]*viewNode {
	return []map[string]*viewNode{
		v.byPath, v.bySession, v.byNamespace, v.byDate, v.byArtifact, v.byOrphaned,
	}
}

// backfill walks the source and classifies each manifest. The
// caller holds no lock; backfill is single-threaded by virtue of
// running before New returns.
//
// Two paths exist:
//
//   - Fast path (an ExtSource is configured). Source.Walk
//     gives us the stripped manifests; we top them up by
//     calling extSource.Ext(id) — an O(log N) lookup against
//     a local index extension that persisted the ext block at
//     write time. No round-trip to Source.Get per manifest.
//
//   - Slow path (no ExtSource). We round-trip Source.Get for
//     every manifest to recover Ext. This is N+1 by
//     construction; acceptable for tests with FakeSource
//     (which keeps full manifests in memory anyway, so Get is
//     cheap) and for backfills small enough that latency
//     doesn't matter. Daemons with large stores configure
//     WithExtSource (or WithFSIndex) to take the fast path.
func (v *View) backfill(ctx context.Context) error {
	cb := func(m domain.Manifest) error {
		if !v.passesFilter(m) {
			return nil
		}
		v.populateExt(ctx, &m)
		v.indexArtifact(m, true /*duringBackfill*/)
		return nil
	}
	if err := v.src.Walk(ctx, "*", cb); err != nil {
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
	// Fast path: bulk ExtSource lookup, no Source round-trip.
	if v.opts.extSource != nil {
		raw, ok, err := v.opts.extSource.Ext(m.ArtifactID)
		if err == nil && ok && len(raw) > 0 {
			m.Ext = raw
			return
		}
		// Miss or error — fall through to Source.Get. A miss is
		// legitimate (artifact written before the extension was
		// registered, or extension doesn't index this schema);
		// for those rare cases the slow path is correct.
	}

	// Slow path: round-trip Source.Get for the full manifest.
	// Walk-style sources (store.Store-backed) usually return a
	// stripped manifest from the index — Ext, layout, inline
	// blob, etc. are absent. Resolvers like fsmeta need Ext to
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
	if f.Namespace != "" && m.Namespace != f.Namespace {
		return false
	}
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

// resolvePathForManifest applies the configured PathResolver, then
// the fallback policy, returning (path, true) when the artifact
// has any by-path representation (real or synthetic), or
// ("", false) when it does not (FallbackOrphaned + no resolver
// path).
func (v *View) resolvePathForManifest(m domain.Manifest) (string, bool) {
	if v.opts.resolver != nil {
		if path, ok := v.opts.resolver(m); ok {
			return path, true
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
//	<namespace>/<sid-shard>/<id-short>.bin   — namespace + session
//	<namespace>/<id-short>.bin               — namespace only
//	_anonymous/<id-short>.bin                — neither
func (v *View) syntheticPath(m domain.Manifest) string {
	idShort := shortID(m.ArtifactID)
	switch {
	case m.Namespace != "" && m.SessionID != "":
		return m.Namespace + "/" + sessionShard(m.SessionID) + "/" + idShort + ".bin"
	case m.Namespace != "":
		return m.Namespace + "/" + idShort + ".bin"
	default:
		return "_anonymous/" + idShort + ".bin"
	}
}

// indexArtifact registers an artifact in every applicable tree.
// duringBackfill controls whether Stats counters are bumped
// (always yes during backfill; on Add it is incremental).
//
// The function does NOT take the View lock — callers (backfill,
// Add) handle locking themselves. backfill runs single-threaded;
// Add takes the write lock around the call.
func (v *View) indexArtifact(m domain.Manifest, duringBackfill bool) {
	rec := &artifactRecord{manifest: m}

	// by-artifact — always.
	rec.pathByArtifact = byArtifactPath(m.ArtifactID)
	v.insertFile(v.byArtifact, rec.pathByArtifact, m)

	// by-date — always.
	rec.pathByDate = byDatePath(m)
	v.insertFile(v.byDate, rec.pathByDate, m)

	// by-namespace — always (synthetic _default for empty Namespace).
	rec.pathByNamespace = byNamespacePath(m)
	v.insertFile(v.byNamespace, rec.pathByNamespace, m)

	// by-session — only if SessionID present.
	if m.SessionID != "" {
		rec.pathBySession = bySessionPath(m)
		v.insertFile(v.bySession, rec.pathBySession, m)
	}

	// by-path — depends on resolver + fallback. When the
	// resolver doesn't produce a path the artifact lands in
	// byOrphaned instead, indexed under the same id-shaped
	// layout byArtifact uses.
	if path, ok := v.resolvePathForManifest(m); ok {
		v.applyByPathInsert(path, m, rec)
	} else {
		rec.pathByOrphaned = byArtifactPath(m.ArtifactID)
		v.insertFile(v.byOrphaned, rec.pathByOrphaned, m)
		v.Stats.OrphanedCount++
	}

	v.artifacts[m.ArtifactID] = rec

	// Stats. TotalNodes counts artifacts (not virtual dirs).
	v.Stats.TotalNodes++
	v.Stats.TotalBytes += m.OriginalSize
	if m.SessionID != "" {
		if _, seen := v.seenSessions[m.SessionID]; !seen {
			v.seenSessions[m.SessionID] = struct{}{}
			v.Stats.SessionCount++
		}
	}
	if m.Namespace != "" {
		if _, seen := v.seenNamespaces[m.Namespace]; !seen {
			v.seenNamespaces[m.Namespace] = struct{}{}
			v.Stats.NamespaceCount++
		}
	}
	_ = duringBackfill
}

// applyByPathInsert handles the collision logic for the by-path
// tree. Three cases:
//
//  1. path is unclaimed → insert as winner.
//  2. path is claimed; newcomer is fresher → newcomer wins,
//     previous owner becomes loser, event.EventPathCollision emitted.
//  3. path is claimed; newcomer is older → newcomer joins the
//     losers list, no by-path node, event.EventPathCollision emitted.
//
// The "fresher" rule is CreatedAt descending; on tie, the
// lexicographically larger ArtifactID wins (deterministic when
// two artifacts are written in the same second).
func (v *View) applyByPathInsert(path string, m domain.Manifest, rec *artifactRecord) {
	rec.pathByPath = path

	currentOwner, claimed := v.pathOwner[path]
	if !claimed {
		v.pathOwner[path] = m.ArtifactID
		v.insertFile(v.byPath, path, m)
		return
	}

	currentRec := v.artifacts[currentOwner]
	if currentRec == nil {
		// Should not happen: pathOwner without artifact record.
		// Recover by treating as unclaimed.
		v.pathOwner[path] = m.ArtifactID
		v.insertFile(v.byPath, path, m)
		return
	}

	if isFresherWinner(m, currentRec.manifest) {
		// Newcomer wins. Demote previous owner.
		v.pathOwner[path] = m.ArtifactID
		v.removeFile(v.byPath, path)
		v.insertFile(v.byPath, path, m)
		v.pushLoser(path, currentRec.manifest)
		v.publish(event.EventPathCollision, event.PathCollisionPayload{
			Path:   path,
			Winner: m.ArtifactID,
			Loser:  currentOwner,
		})
		v.Stats.CollisionCount++
		return
	}

	// Newcomer loses.
	v.pushLoser(path, m)
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

// pushLoser inserts an entry into pathLosers[path], keeping the
// slice sorted by CreatedAt descending (and ArtifactID descending
// on tie). pathLosers[path] is allocated lazily.
func (v *View) pushLoser(path string, m domain.Manifest) {
	losers := v.pathLosers[path]
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
	v.pathLosers[path] = losers
}

// removeLoser drops the entry with the given id from
// pathLosers[path]; no-op if not present.
func (v *View) removeLoser(path string, id domain.ArtifactID) {
	losers := v.pathLosers[path]
	for i, l := range losers {
		if l.id == id {
			v.pathLosers[path] = append(losers[:i], losers[i+1:]...)
			if len(v.pathLosers[path]) == 0 {
				delete(v.pathLosers, path)
			}
			return
		}
	}
}
