package projection

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/event"
	"github.com/rkurbatov/scrinium/projection/fsmeta"
)

// View is the read side of the projection. It holds five
// parallel in-memory trees (by-path, by-session, by-namespace,
// by-date, by-artifact) populated by backfill at NewView time.
//
// Concurrency: every public method takes the View's RWMutex.
// Add/Remove/Move take the write lock; readers take a read lock.
// All read methods build a private copy of any state they iterate
// over, so callers can do their own work without holding the
// projection lock.
//
// Tree access: trees are exposed through symmetric methods —
// GetByPath/ListByPath/WalkByPath/OpenByPath, GetBySession/...,
// etc. The View has no notion of a single "current root"; the
// transport layer (FUSE, WebDAV) decides which tree to surface in
// the mount root and which to hide under a service prefix.
type View struct {
	// Public, read-only after NewView returns.
	Source    SourceKind
	CreatedAt time.Time
	Stats     ViewStats

	// Internal state, guarded by mu.
	mu sync.RWMutex

	// Trees. Each map keys by full path (no leading slash, ""
	// means tree root). Values are the canonical viewNode for
	// that path.
	byPath      map[string]*viewNode
	bySession   map[string]*viewNode
	byNamespace map[string]*viewNode
	byDate      map[string]*viewNode
	byArtifact  map[string]*viewNode
	// byOrphaned holds artifacts the path resolver couldn't
	// place — typically system manifests or artifacts written
	// without an fsmeta payload. Same id-shaped layout as
	// byArtifact (aa/bb/<id>) so the same lookup helpers work.
	// Unlike byArtifact (which contains every artifact), this
	// one only contains the ones missing from byPath.
	byOrphaned map[string]*viewNode

	// Per-artifact tracking. Used by Remove/Move to fan out a
	// deletion or move across every tree without re-deriving the
	// paths.
	artifacts map[domain.ArtifactID]*artifactRecord

	// Path-collision bookkeeping for by-path. pathOwner records the
	// ArtifactID currently holding each path; pathLosers stores
	// every other ArtifactID claiming the same path, sorted by
	// CreatedAt descending so the freshest loser is at index 0.
	// On Remove of the current owner we promote pathLosers[path][0]
	// to owner; on a new Add against an existing owner we compare
	// CreatedAt to decide whether the newcomer becomes owner or
	// joins the losers list.
	pathOwner  map[string]domain.ArtifactID
	pathLosers map[string][]loserEntry

	// For Stats: track unique sessions and namespaces seen.
	seenSessions   map[string]struct{}
	seenNamespaces map[string]struct{}

	source ProjectionSource
	bus    event.EventBus // nil = events not published
	opts   viewOptions
	closed atomic.Bool
}

// viewNode is the internal node representation. The public Node
// is built from these fields when read.
type viewNode struct {
	fs       FilesystemFacet
	artifact *ArtifactFacet // nil for virtual directories
	children []string       // sorted last-segment names; nil for files
}

// artifactRecord is the cross-tree record of an artifact: the
// manifest plus the path under which it appears in every tree.
// Empty paths mean the artifact is absent from that tree (e.g.,
// no by-path entry when Resolver returned !ok and fallback is
// orphaned).
type artifactRecord struct {
	manifest        domain.Manifest
	pathByArtifact  string
	pathBySession   string
	pathByNamespace string
	pathByDate      string
	pathByPath      string // "" if artifact is orphaned
	pathByOrphaned  string // "" if artifact is in byPath
}

// loserEntry records a losing artifact in a path collision —
// the ArtifactID and its CreatedAt for re-election ordering.
type loserEntry struct {
	id        domain.ArtifactID
	createdAt time.Time
}

// --- Construction ---

// NewView constructs a View by walking source and populating
// every tree. Backfill is synchronous: NewView returns only after
// the source has been fully traversed.
//
// Default options:
//   - root view: RootByPath (informational only)
//   - fallback: FallbackOrphaned
//   - filter: empty
//
// EventBus is optional via WithEventBus; without it the View
// silently produces no events.
func NewView(ctx context.Context, source ProjectionSource, opts ...ViewOption) (*View, error) {
	if source == nil {
		return nil, fmt.Errorf("projection.NewView: source is nil")
	}

	o := viewOptions{
		rootView: RootByPath,
		fallback: FallbackOrphaned,
	}
	for _, opt := range opts {
		opt(&o)
	}

	v := &View{
		Source:    SourceKindStore,
		CreatedAt: time.Now().UTC(),

		source: source,
		bus:    o.bus,
		opts:   o,

		byPath:      make(map[string]*viewNode),
		bySession:   make(map[string]*viewNode),
		byNamespace: make(map[string]*viewNode),
		byDate:      make(map[string]*viewNode),
		byArtifact:  make(map[string]*viewNode),
		byOrphaned:  make(map[string]*viewNode),

		artifacts:  make(map[domain.ArtifactID]*artifactRecord),
		pathOwner:  make(map[string]domain.ArtifactID),
		pathLosers: make(map[string][]loserEntry),

		seenSessions:   make(map[string]struct{}),
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
	v.publish(EventViewRebuilt, ViewRebuiltPayload{
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
// running before NewView returns.
//
// Two paths exist:
//
//   - Fast path (a MetadataSource is configured). Source.Walk
//     gives us the stripped manifests; we top them up by calling
//     metadataSource.Metadata(id) — an O(log N) lookup against a
//     local index extension that persisted Metadata at write
//     time. No round-trip to Source.Get per manifest.
//
//   - Slow path (no MetadataSource). We round-trip Source.Get
//     for every manifest to recover Metadata. This is N+1 by
//     construction; acceptable for tests with FakeSource (which
//     keeps full manifests in memory anyway, so Get is cheap)
//     and for backfills small enough that latency doesn't
//     matter. Daemons with large stores configure
//     WithMetadataSource (or WithFSIndex) to take the fast path.
func (v *View) backfill(ctx context.Context) error {
	cb := func(m domain.Manifest) error {
		if !v.passesFilter(m) {
			return nil
		}
		v.populateMetadata(ctx, &m)
		v.indexArtifact(m, true /*duringBackfill*/)
		return nil
	}
	if err := v.source.Walk(ctx, "*", cb); err != nil {
		return fmt.Errorf("%w: %w", errs.ErrSourceUnavailable, err)
	}
	return nil
}

// populateMetadata fills m.Metadata (and a couple of cheap
// neighbouring fields) from the configured fast path or, failing
// that, from Source.Get. Mutates m in place.
//
// Errors from either path are intentionally swallowed — backfill
// must finish even if one manifest's metadata fetch fails (the
// resolver will then treat it as orphaned, the standard
// missing-path behaviour). A noisy "best effort" log line could
// be added behind the bus once we wire it.
func (v *View) populateMetadata(ctx context.Context, m *domain.Manifest) {
	// Fast path: bulk MetadataSource lookup, no Source round-trip.
	if v.opts.metadataSource != nil {
		raw, ok, err := v.opts.metadataSource.Metadata(m.ArtifactID)
		if err == nil && ok && len(raw) > 0 {
			m.Metadata = raw
			return
		}
		// Miss or error — fall through to Source.Get. A miss is
		// legitimate (artifact written before the extension was
		// registered, or extension doesn't index this schema);
		// for those rare cases the slow path is correct.
	}

	// Slow path: round-trip Source.Get for the full manifest.
	// Walk-style sources (core.Store-backed) usually return a
	// stripped manifest from the index — Metadata, layout,
	// inline blob, etc. are absent. Resolvers like fsmeta need
	// Metadata to produce a path.
	//
	// FakeSource and similar in-memory test stubs already return
	// complete manifests; doing a Get on top is a cheap no-op.
	rh, err := v.source.Get(ctx, m.ArtifactID, domain.GetOptions{})
	if err != nil {
		return
	}
	full := rh.Manifest()
	rh.Close()
	// Preserve any fields Walk had set that Get's manifest may
	// lack (rare in practice — Get is the authoritative source —
	// but cheap to be safe).
	if len(full.Metadata) > 0 {
		m.Metadata = full.Metadata
	}
	if full.ContentHash != "" {
		m.ContentHash = full.ContentHash
	}
	if full.OriginalSize != 0 {
		m.OriginalSize = full.OriginalSize
	}
}

// passesFilter checks the configured ViewFilter against a
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
// duringBackfill controls whether ViewStats counters are bumped
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
//     previous owner becomes loser, EventPathCollision emitted.
//  3. path is claimed; newcomer is older → newcomer joins the
//     losers list, no by-path node, EventPathCollision emitted.
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
		v.publish(EventPathCollision, PathCollisionPayload{
			Path:   path,
			Winner: m.ArtifactID,
			Loser:  currentOwner,
		})
		v.Stats.CollisionCount++
		return
	}

	// Newcomer loses.
	v.pushLoser(path, m)
	v.publish(EventPathCollision, PathCollisionPayload{
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

// --- Public accessors ---

// RootView returns the configured root tree. It is informational
// metadata: the View itself does not hide other trees, but
// transports (FUSE, FSOps) read this to decide which tree to
// surface in the mount root and which to relegate to the service
// directory.
//
// Stable for the lifetime of the View — the option is set at
// NewView time and never mutated.
func (v *View) RootView() RootView { return v.opts.rootView }

// --- Read methods (one set per tree) ---

// Locations bundles every tree-placement of one artifact —
// what the web "Locations" panel shows. Empty fields (e.g.
// PathByPath="" for orphaned, PathByOrphaned="" for placed)
// signal "this tree doesn't carry this artifact".
type Locations struct {
	ByArtifact  string
	BySession   string
	ByNamespace string
	ByDate      string
	ByPath      string // "" if orphaned
	ByOrphaned  string // "" if placed under byPath
}

// LookupLocations returns the per-tree paths of an artifact.
// Used by the web artifact details page to surface "show me
// where this lives" links into each tree's listing.
//
// (zero, false) if the artifact isn't tracked.
func (v *View) LookupLocations(id domain.ArtifactID) (Locations, bool) {
	if v.closed.Load() {
		return Locations{}, false
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	rec, ok := v.artifacts[id]
	if !ok {
		return Locations{}, false
	}
	return Locations{
		ByArtifact:  rec.pathByArtifact,
		BySession:   rec.pathBySession,
		ByNamespace: rec.pathByNamespace,
		ByDate:      rec.pathByDate,
		ByPath:      rec.pathByPath,
		ByOrphaned:  rec.pathByOrphaned,
	}, true
}

// SearchResult is one hit returned by View.Search. Carries
// enough fields to render a result row without forcing the
// caller to follow up with manifest lookups.
type SearchResult struct {
	ArtifactID  domain.ArtifactID
	Path        string // by-path placement; empty if orphaned
	Namespace   string
	SessionID   string
	CreatedAt   time.Time
	MIME        string // from fsmeta when present
	MatchReason string // "path" | "namespace" | "id"
}

// Search scans the View for artifacts matching the query.
// Substring matching, case-insensitive, against:
//
//   - the artifact's by-path placement (covers fsmeta names);
//   - the namespace field;
//   - an exact ArtifactID match (so users can paste an id and
//     jump straight to it).
//
// limit caps the result count; passing 0 disables the cap (use
// only for diagnostic flows). Order matches the scan order over
// the artifacts map — random-but-stable within a single View
// state. Callers sort if they need a specific order.
//
// Implementation is the same linear scan as RelatedByBlobRef:
// O(N) under RLock, fast for stores up to ~100K artifacts.
// Beyond that, we'd want an actual search index — see backlog.
func (v *View) Search(query string, limit int) []SearchResult {
	if v.closed.Load() {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}

	v.mu.RLock()
	defer v.mu.RUnlock()

	var out []SearchResult
	for id, rec := range v.artifacts {
		// Exact id match — strongest signal, surface first.
		if string(id) == query {
			out = append(out, makeSearchResult(id, rec, "id"))
			if limit > 0 && len(out) >= limit {
				return out
			}
			continue
		}

		path := strings.ToLower(rec.pathByPath)
		ns := strings.ToLower(rec.manifest.Namespace)

		switch {
		case path != "" && strings.Contains(path, q):
			out = append(out, makeSearchResult(id, rec, "path"))
		case ns != "" && strings.Contains(ns, q):
			out = append(out, makeSearchResult(id, rec, "namespace"))
		default:
			continue
		}
		if limit > 0 && len(out) >= limit {
			return out
		}
	}
	return out
}

// makeSearchResult populates a SearchResult from an artifact
// record. MIME is best-effort from fsmeta; absence falls back
// to empty (the UI is responsible for any extension-based
// inference it cares about).
func makeSearchResult(id domain.ArtifactID, rec *artifactRecord, reason string) SearchResult {
	r := SearchResult{
		ArtifactID:  id,
		Path:        rec.pathByPath,
		Namespace:   rec.manifest.Namespace,
		SessionID:   rec.manifest.SessionID,
		CreatedAt:   rec.manifest.CreatedAt,
		MatchReason: reason,
	}
	if fs, ok, err := fsmeta.Decode(rec.manifest.Metadata); err == nil && ok {
		r.MIME = fs.MIME
	}
	return r
}

func (v *View) GetByPath(path string) (Node, error)      { return v.getInTree(v.byPath, path) }
func (v *View) GetBySession(path string) (Node, error)   { return v.getInTree(v.bySession, path) }
func (v *View) GetByNamespace(path string) (Node, error) { return v.getInTree(v.byNamespace, path) }
func (v *View) GetByDate(path string) (Node, error)      { return v.getInTree(v.byDate, path) }
func (v *View) GetByArtifact(path string) (Node, error)  { return v.getInTree(v.byArtifact, path) }
func (v *View) GetByOrphaned(path string) (Node, error)  { return v.getInTree(v.byOrphaned, path) }

// RelatedArtifact is the small descriptor returned by
// RelatedByBlobRef. Carries enough fields for a UI to render
// "where else this blob lives" without forcing the caller to
// follow up with manifest lookups.
type RelatedArtifact struct {
	ArtifactID domain.ArtifactID
	Path       string // by-path placement; empty if orphaned
	Namespace  string
	SessionID  string
	CreatedAt  time.Time
}

// RelatedByBlobRef returns every artifact that shares the given
// BlobRef, excluding the artifact identified by `exclude`.
// Useful for the "this blob is also used here" web view —
// one of the few introspections specific to a CAS store.
//
// Implementation is a linear scan of the artifacts map. That
// scales to roughly 100K artifacts inside a single web request
// without blocking; bigger stores will want an index by
// blob_ref. We accept the linearity now because the alternative
// (push the query into core.Store/index) costs more wiring than
// the value justifies at this scale.
//
// Concurrency: holds RLock for the scan duration. A
// long-running scan would block writers; the 100K-artifact
// budget keeps it under ~10ms in practice.
func (v *View) RelatedByBlobRef(blobRef domain.BlobRef, exclude domain.ArtifactID) []RelatedArtifact {
	if v.closed.Load() {
		return nil
	}
	v.mu.RLock()
	defer v.mu.RUnlock()

	var out []RelatedArtifact
	for id, rec := range v.artifacts {
		if id == exclude {
			continue
		}
		if rec.manifest.BlobRef != blobRef {
			continue
		}
		out = append(out, RelatedArtifact{
			ArtifactID: id,
			Path:       rec.pathByPath,
			Namespace:  rec.manifest.Namespace,
			SessionID:  rec.manifest.SessionID,
			CreatedAt:  rec.manifest.CreatedAt,
		})
	}
	return out
}

func (v *View) ListByPath(path string) NodeSeq      { return v.listInTree(v.byPath, path) }
func (v *View) ListBySession(path string) NodeSeq   { return v.listInTree(v.bySession, path) }
func (v *View) ListByNamespace(path string) NodeSeq { return v.listInTree(v.byNamespace, path) }
func (v *View) ListByDate(path string) NodeSeq      { return v.listInTree(v.byDate, path) }
func (v *View) ListByArtifact(path string) NodeSeq  { return v.listInTree(v.byArtifact, path) }
func (v *View) ListByOrphaned(path string) NodeSeq  { return v.listInTree(v.byOrphaned, path) }

func (v *View) WalkByPath(prefix string) NodeSeq      { return v.walkInTree(v.byPath, prefix) }
func (v *View) WalkBySession(prefix string) NodeSeq   { return v.walkInTree(v.bySession, prefix) }
func (v *View) WalkByNamespace(prefix string) NodeSeq { return v.walkInTree(v.byNamespace, prefix) }
func (v *View) WalkByDate(prefix string) NodeSeq      { return v.walkInTree(v.byDate, prefix) }
func (v *View) WalkByArtifact(prefix string) NodeSeq  { return v.walkInTree(v.byArtifact, prefix) }
func (v *View) WalkByOrphaned(prefix string) NodeSeq  { return v.walkInTree(v.byOrphaned, prefix) }

func (v *View) OpenByPath(ctx context.Context, path string, opts domain.GetOptions) (core.ReadHandle, error) {
	return v.openInTree(ctx, v.byPath, path, opts)
}
func (v *View) OpenBySession(ctx context.Context, path string, opts domain.GetOptions) (core.ReadHandle, error) {
	return v.openInTree(ctx, v.bySession, path, opts)
}
func (v *View) OpenByNamespace(ctx context.Context, path string, opts domain.GetOptions) (core.ReadHandle, error) {
	return v.openInTree(ctx, v.byNamespace, path, opts)
}
func (v *View) OpenByDate(ctx context.Context, path string, opts domain.GetOptions) (core.ReadHandle, error) {
	return v.openInTree(ctx, v.byDate, path, opts)
}
func (v *View) OpenByArtifact(ctx context.Context, path string, opts domain.GetOptions) (core.ReadHandle, error) {
	return v.openInTree(ctx, v.byArtifact, path, opts)
}
func (v *View) OpenByOrphaned(ctx context.Context, path string, opts domain.GetOptions) (core.ReadHandle, error) {
	return v.openInTree(ctx, v.byOrphaned, path, opts)
}

// --- Root-view dispatchers ---
//
// GetIn, ListIn and OpenIn select the appropriate per-tree
// method based on a RootView enum. Used by callers that already
// hold a RootView value (FSOps when routing through the
// configured root, scrinium-fuse when serving _scrinium/<tree>/
// service paths) instead of branching on the enum themselves.
//
// An unknown RootView returns ErrPathNotFound for Get/Open and
// a single-shot error sequence for List, matching the behaviour
// service callers expect when a configuration drifts.

// GetIn dispatches GetByX based on rv.
func (v *View) GetIn(rv RootView, path string) (Node, error) {
	tree := v.treeFor(rv)
	if tree == nil {
		return Node{}, errs.ErrPathNotFound
	}
	return v.getInTree(tree, path)
}

// ListIn dispatches ListByX based on rv.
func (v *View) ListIn(rv RootView, path string) NodeSeq {
	tree := v.treeFor(rv)
	if tree == nil {
		return func(yield func(Node, error) bool) {
			yield(Node{}, errs.ErrPathNotFound)
		}
	}
	return v.listInTree(tree, path)
}

// OpenIn dispatches OpenByX based on rv.
func (v *View) OpenIn(ctx context.Context, rv RootView, path string, opts domain.GetOptions) (core.ReadHandle, error) {
	tree := v.treeFor(rv)
	if tree == nil {
		return nil, errs.ErrPathNotFound
	}
	return v.openInTree(ctx, tree, path, opts)
}

// treeFor returns the internal tree for the given RootView, or
// nil for an unknown enum value. Private — outside callers go
// through GetIn/ListIn/OpenIn, which absorb the nil check.
func (v *View) treeFor(rv RootView) map[string]*viewNode {
	switch rv {
	case RootByPath:
		return v.byPath
	case RootBySession:
		return v.bySession
	case RootByNamespace:
		return v.byNamespace
	case RootByDate:
		return v.byDate
	case RootByArtifact:
		return v.byArtifact
	case RootByOrphaned:
		return v.byOrphaned
	}
	return nil
}

// --- Per-tree implementations ---

func (v *View) getInTree(tree map[string]*viewNode, path string) (Node, error) {
	if v.closed.Load() {
		return Node{}, errs.ErrViewClosed
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	n, ok := tree[path]
	if !ok {
		return Node{}, fmt.Errorf("%w: %q", errs.ErrPathNotFound, path)
	}
	return v.exportNode(n), nil
}

func (v *View) listInTree(tree map[string]*viewNode, path string) NodeSeq {
	return func(yield func(Node, error) bool) {
		if v.closed.Load() {
			yield(Node{}, errs.ErrViewClosed)
			return
		}
		v.mu.RLock()
		defer v.mu.RUnlock()

		n, ok := tree[path]
		if !ok {
			yield(Node{}, fmt.Errorf("%w: %q", errs.ErrPathNotFound, path))
			return
		}
		if !n.fs.IsDir {
			yield(Node{}, fmt.Errorf("%w: %q", errs.ErrNotADirectory, path))
			return
		}
		names := append([]string(nil), n.children...)
		for _, name := range names {
			childPath := name
			if path != "" {
				childPath = path + "/" + name
			}
			child, ok := tree[childPath]
			if !ok {
				continue
			}
			if !yield(v.exportNode(child), nil) {
				return
			}
		}
	}
}

func (v *View) walkInTree(tree map[string]*viewNode, prefix string) NodeSeq {
	return func(yield func(Node, error) bool) {
		if v.closed.Load() {
			yield(Node{}, errs.ErrViewClosed)
			return
		}
		v.mu.RLock()
		defer v.mu.RUnlock()

		root, ok := tree[prefix]
		if !ok {
			yield(Node{}, fmt.Errorf("%w: %q", errs.ErrPathNotFound, prefix))
			return
		}
		var stack []*viewNode
		stack = append(stack, root)
		for len(stack) > 0 {
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if !yield(v.exportNode(n), nil) {
				return
			}
			if n.fs.IsDir {
				for i := len(n.children) - 1; i >= 0; i-- {
					name := n.children[i]
					childPath := name
					if n.fs.Path != "" {
						childPath = n.fs.Path + "/" + name
					}
					if c, ok := tree[childPath]; ok {
						stack = append(stack, c)
					}
				}
			}
		}
	}
}

func (v *View) openInTree(
	ctx context.Context,
	tree map[string]*viewNode,
	path string,
	opts domain.GetOptions,
) (core.ReadHandle, error) {
	if v.closed.Load() {
		return nil, errs.ErrViewClosed
	}
	v.mu.RLock()
	n, ok := tree[path]
	v.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %q", errs.ErrPathNotFound, path)
	}
	if n.fs.IsDir {
		return nil, fmt.Errorf("%w: %q", errs.ErrIsADirectory, path)
	}
	rh, err := v.source.Get(ctx, n.artifact.ArtifactID, opts)
	if err != nil {
		return nil, mapSourceError(err)
	}
	return rh, nil
}

// --- Mutation methods ---

// Close marks the View closed. Idempotent. Subsequent reads
// return ErrViewClosed.
func (v *View) Close() error {
	v.closed.Store(true)
	return nil
}

// Add registers a new manifest, mirroring backfill's per-manifest
// path. Used by FSOps after Store.Put. Concurrent with reads;
// holds the write lock.
//
// Returns ErrViewClosed if the View is closed. Otherwise nil —
// classification cannot fail for a valid manifest (the input
// itself is what the source produced).
func (v *View) Add(m domain.Manifest) error {
	if v.closed.Load() {
		return errs.ErrViewClosed
	}
	if !v.passesFilter(m) {
		return nil
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	// Idempotent: an Add for an already-known ArtifactID is a no-op.
	if _, exists := v.artifacts[m.ArtifactID]; exists {
		return nil
	}
	v.indexArtifact(m, false)
	return nil
}

// Remove drops every entry of the artifact from every tree.
// Handles by-path collision re-election when the removed
// artifact was the current owner of a path.
//
// Idempotent: Remove for an unknown ArtifactID is a no-op.
func (v *View) Remove(id domain.ArtifactID) error {
	if v.closed.Load() {
		return errs.ErrViewClosed
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	rec, ok := v.artifacts[id]
	if !ok {
		return nil
	}
	v.removeArtifactFromTrees(id, rec)
	return nil
}

// removeArtifactFromTrees does the actual fan-out delete. Caller
// holds the write lock.
func (v *View) removeArtifactFromTrees(id domain.ArtifactID, rec *artifactRecord) {
	if rec.pathByArtifact != "" {
		v.removeFile(v.byArtifact, rec.pathByArtifact)
	}
	if rec.pathByDate != "" {
		v.removeFile(v.byDate, rec.pathByDate)
	}
	if rec.pathByNamespace != "" {
		v.removeFile(v.byNamespace, rec.pathByNamespace)
	}
	if rec.pathBySession != "" {
		v.removeFile(v.bySession, rec.pathBySession)
	}
	if rec.pathByPath != "" {
		v.removeFromByPath(id, rec)
	}
	if rec.pathByOrphaned != "" {
		v.removeFile(v.byOrphaned, rec.pathByOrphaned)
	}

	delete(v.artifacts, id)
	v.Stats.TotalNodes--
	v.Stats.TotalBytes -= rec.manifest.OriginalSize
	if rec.pathByPath == "" {
		v.Stats.OrphanedCount--
	}
	// SessionCount and NamespaceCount: we do not decrement because
	// tracking "last artifact in this session" requires a counter
	// per session, which is a 3b-future complication. Stats remain
	// monotonic for those two counters across the View's lifetime —
	// callers use them for pacing, not for exact accounting.
}

// removeFromByPath drops an artifact from the by-path tree. If
// it was the current owner of a path, the freshest loser (if any)
// is promoted to owner.
func (v *View) removeFromByPath(id domain.ArtifactID, rec *artifactRecord) {
	path := rec.pathByPath
	owner, claimed := v.pathOwner[path]
	if claimed && owner == id {
		// Drop the file node and try to promote a loser.
		v.removeFile(v.byPath, path)
		delete(v.pathOwner, path)
		losers := v.pathLosers[path]
		if len(losers) > 0 {
			promoted := losers[0]
			v.pathLosers[path] = losers[1:]
			if len(v.pathLosers[path]) == 0 {
				delete(v.pathLosers, path)
			}
			promotedRec, ok := v.artifacts[promoted.id]
			if ok {
				v.pathOwner[path] = promoted.id
				v.insertFile(v.byPath, path, promotedRec.manifest)
			}
		}
	} else {
		// Removed artifact was a loser, not owner.
		v.removeLoser(path, id)
	}
}

// Move atomically replaces an old artifact with a new one — used
// by FSOps to emulate rename. The old artifact's by-path entry
// is dropped (with collision re-election), and the new manifest
// is added through the standard Add path.
//
// oldPath/newPath are passed for documentation and future use
// (FSOps wants to log the user-level rename); the actual location
// in by-path comes from the new manifest's resolver result.
func (v *View) Move(oldPath, newPath string, m domain.Manifest) error {
	if v.closed.Load() {
		return errs.ErrViewClosed
	}
	v.mu.Lock()
	defer v.mu.Unlock()

	// We do not require oldPath to currently exist — the FSOps
	// orchestration may have already done the Store.Delete and
	// only failed to find the manifest. Move is idempotent on the
	// "old" side: remove if present, add new.

	// Find old artifact by oldPath in by-path; if found, remove.
	if oldOwner, ok := v.pathOwner[oldPath]; ok {
		if rec, found := v.artifacts[oldOwner]; found {
			v.removeArtifactFromTrees(oldOwner, rec)
		}
	}

	// Add the new manifest, applying filter.
	if !v.passesFilter(m) {
		return nil
	}
	if _, exists := v.artifacts[m.ArtifactID]; exists {
		return nil
	}
	v.indexArtifact(m, false)
	_ = newPath
	return nil
}

// --- Internal helpers ---

// insertFile creates a file node (or updates an existing one) at
// path in tree, ensuring all parent directories exist as virtual
// nodes.
//
// FilesystemFacet carries only the schema-agnostic fields: Name,
// Path, Size, ModTime, IsDir. POSIX attributes (mode/uid/gid)
// live in fsmeta.FileSystem inside Manifest.Metadata and are
// materialised by FSOps at the transport boundary.
//
// ModTime here is seeded from m.CreatedAt as a baseline; FSOps
// overrides with fsmeta.ModTime when non-zero.
func (v *View) insertFile(tree map[string]*viewNode, path string, m domain.Manifest) {
	v.ensureDirs(tree, parentPath(path))
	name := lastSegment(path)
	tree[path] = &viewNode{
		fs: FilesystemFacet{
			Name:    name,
			Path:    path,
			IsDir:   false,
			Size:    m.OriginalSize,
			ModTime: m.CreatedAt,
		},
		artifact: artifactFacetFrom(m),
	}
	parent := parentPath(path)
	if pn, ok := tree[parent]; ok {
		pn.children = insertSorted(pn.children, name)
	}
}

// removeFile deletes the node at path. Empty parent directories
// are recursively pruned to keep List tidy. The tree root ""
// always survives.
func (v *View) removeFile(tree map[string]*viewNode, path string) {
	if _, ok := tree[path]; !ok {
		return
	}
	delete(tree, path)
	parent := parentPath(path)
	name := lastSegment(path)
	if pn, ok := tree[parent]; ok {
		pn.children = removeSorted(pn.children, name)
		// Prune empty virtual directory cascading upwards.
		for parent != "" && len(pn.children) == 0 && pn.artifact == nil {
			delete(tree, parent)
			grand := parentPath(parent)
			gname := lastSegment(parent)
			parent = grand
			pn, ok = tree[grand]
			if !ok {
				break
			}
			pn.children = removeSorted(pn.children, gname)
		}
	}
}

// ensureDirs walks path top-down and inserts virtual directory
// nodes for every component that does not yet exist.
func (v *View) ensureDirs(tree map[string]*viewNode, path string) {
	if path == "" {
		return
	}
	segments := strings.Split(path, "/")
	cur := ""
	for i, seg := range segments {
		next := seg
		if cur != "" {
			next = cur + "/" + seg
		}
		if _, ok := tree[next]; !ok {
			tree[next] = newDirNode(seg, next, v.CreatedAt)
			parent := ""
			if i > 0 {
				parent = cur
			}
			if pn, ok := tree[parent]; ok {
				pn.children = insertSorted(pn.children, seg)
			}
		}
		cur = next
	}
}

// newDirNode creates an empty virtual-directory node. POSIX
// mode/uid/gid live in FSOps defaults — virtual directories
// have no metadata of their own.
func newDirNode(name, path string, modTime time.Time) *viewNode {
	return &viewNode{
		fs: FilesystemFacet{
			Name:    name,
			Path:    path,
			IsDir:   true,
			ModTime: modTime,
		},
	}
}

// artifactFacetFrom builds the Node.Artifact facet from a manifest.
func artifactFacetFrom(m domain.Manifest) *ArtifactFacet {
	return &ArtifactFacet{
		ArtifactID:  m.ArtifactID,
		ContentHash: m.ContentHash,
		BlobRef:     m.BlobRef,
		Namespace:   m.Namespace,
		SessionID:   m.SessionID,
		CreatedAt:   m.CreatedAt,
		Type:        m.Type,
		Metadata:    m.Metadata,
	}
}

// exportNode builds the public Node from the internal viewNode.
// Caller holds the read lock.
func (v *View) exportNode(n *viewNode) Node {
	out := Node{FS: n.fs}
	if n.artifact != nil {
		af := *n.artifact
		out.Artifact = &af
	}
	return out
}

// publish emits an event when an EventBus is configured. Keeps
// callers' code path event-agnostic.
func (v *View) publish(eventType string, payload any) {
	if v.bus == nil {
		return
	}
	v.bus.Publish(event.Event{Type: eventType, Payload: payload})
}

// --- Error mapping ---

func mapSourceError(err error) error {
	switch {
	case errors.Is(err, errs.ErrArtifactNotFound):
		return fmt.Errorf("%w: %w", errs.ErrPathNotFound, err)
	case errors.Is(err, errs.ErrLocked),
		errors.Is(err, errs.ErrCorruptedManifest),
		errors.Is(err, errs.ErrCorruptedBlob):
		return fmt.Errorf("%w: %w", errs.ErrArtifactUnreadable, err)
	default:
		return fmt.Errorf("%w: %w", errs.ErrSourceUnavailable, err)
	}
}

// --- Path-building helpers ---

// byArtifactPath: <aa>/<bb>/<full-id>
func byArtifactPath(id domain.ArtifactID) string {
	hash := hashPart(string(id))
	if len(hash) < 4 {
		return "_short/" + string(id)
	}
	return hash[:2] + "/" + hash[2:4] + "/" + string(id)
}

// byDatePath: <YYYY>/<MM>/<DD>/<HH-MM-SS>-<id-short>.bin
// byDatePath builds the by-date layout: <YYYY>/<MM>/<DD>/<HH-MM-SS>-<name>.
// The trailing name is the fsmeta path's basename when available
// (so the listing shows "12-34-56-sunset.jpg") or a short artifact
// id otherwise (for non-fsmeta artifacts that have no human name).
//
// Time resolution is 1 second; same-second artifacts get a dash-id
// suffix appended via the basename which is always unique.
func byDatePath(m domain.Manifest) string {
	t := m.CreatedAt.UTC()
	name := byDateLabel(m)
	return fmt.Sprintf("%04d/%02d/%02d/%02d-%02d-%02d-%s",
		t.Year(), t.Month(), t.Day(),
		t.Hour(), t.Minute(), t.Second(),
		name)
}

// byDateLabel picks the human-friendly suffix for a by-date path.
// Priority: fsmeta path basename → short artifact id with ".bin"
// extension. Two artifacts created in the same second with the
// same fsmeta basename collide; that's accepted — the by-date
// tree is a diagnostic aid, not an authoritative storage layout.
func byDateLabel(m domain.Manifest) string {
	if fs, ok, err := fsmeta.Decode(m.Metadata); err == nil && ok {
		base := pathLastSegment(fs.Path)
		if base != "" {
			return base
		}
	}
	return shortID(m.ArtifactID) + ".bin"
}

// pathLastSegment returns everything after the last "/" in p,
// or p itself if there's no slash. Empty path returns "".
func pathLastSegment(p string) string {
	if p == "" {
		return ""
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

// byNamespacePath: <ns>/<aa>/<bb>/<id>
//
// Empty namespace is bucketed under "_default" so the artifact
// remains visible in the tree.
func byNamespacePath(m domain.Manifest) string {
	ns := m.Namespace
	if ns == "" {
		ns = "_default"
	}
	hash := hashPart(string(m.ArtifactID))
	if len(hash) < 4 {
		return ns + "/_short/" + string(m.ArtifactID)
	}
	return ns + "/" + hash[:2] + "/" + hash[2:4] + "/" + string(m.ArtifactID)
}

// bySessionPath: <aa>/<bb>/<sid>/<artifact-id>
//
// Sessions shorter than 4 characters bucket under "_short/<sid>/...".
// Caller must check m.SessionID != "" before calling.
func bySessionPath(m domain.Manifest) string {
	// Flat layout: <session>/<artifactID>. Earlier versions
	// sharded like by-artifact (xx/yy/sid/...) for forward
	// scalability, but in practice session counts stay tiny
	// (one per process restart) and the sharding only
	// obscured the listing for human inspection.
	sid := m.SessionID
	if sid == "" {
		// Defensive: callers gate this with m.SessionID != ""
		// before invoking, but guard against drift.
		sid = "_no_session"
	}
	return sid + "/" + string(m.ArtifactID)
}

// sessionShard returns the first-segment shard for a SessionID.
// Used by syntheticPath; format mirrors bySessionPath's prefix.
func sessionShard(sid string) string {
	if len(sid) < 4 {
		return "_short/" + sid
	}
	return sid[:2] + "/" + sid[2:4] + "/" + sid
}

// shortID returns the first 16 hex characters of the hash part of
// an ArtifactID. Used by by-date filenames and synthetic paths.
func shortID(id domain.ArtifactID) string {
	hash := hashPart(string(id))
	if len(hash) > 16 {
		return hash[:16]
	}
	return hash
}

// hashPart strips the algorithm prefix from an identifier of
// the form "<algo>-<hex>".
func hashPart(id string) string {
	if i := strings.IndexByte(id, '-'); i >= 0 {
		return id[i+1:]
	}
	return id
}

func parentPath(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

func lastSegment(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return p
	}
	return p[i+1:]
}

// insertSorted inserts name into a sorted slice (idempotent).
func insertSorted(s []string, name string) []string {
	idx := sort.SearchStrings(s, name)
	if idx < len(s) && s[idx] == name {
		return s
	}
	s = append(s, "")
	copy(s[idx+1:], s[idx:])
	s[idx] = name
	return s
}

// removeSorted removes name from a sorted slice.
func removeSorted(s []string, name string) []string {
	idx := sort.SearchStrings(s, name)
	if idx >= len(s) || s[idx] != name {
		return s
	}
	return append(s[:idx], s[idx+1:]...)
}
