package view

import (
	"context"
	"fmt"
	"sort"
	"time"

	"scrinium.dev/domain"
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
