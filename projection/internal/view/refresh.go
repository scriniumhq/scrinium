package view

import (
	"context"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/event"
	"scrinium.dev/projection/internal/source"
)

// refreshIfStale brings the View up to date with the backend before a read
// when another client has written since the last (re)build (ADR-107). It is
// the lazy convergence path: a cheap Token probe on every read, and a full
// re-backfill only when the token moved.
//
// Snapshot mode (no TokenSource, INV-107-6) and probe failures leave the
// cached view in place. The hot path — token unchanged — takes no lock.
// Callers MUST invoke this BEFORE acquiring the read lock; the rebuild takes
// the write lock, so calling it under RLock would deadlock.
func (v *View) refreshIfStale(ctx context.Context) {
	if v.tokenSrc == nil || v.closed.Load() {
		return
	}
	cur, err := v.tokenSrc.Token(ctx)
	if err != nil {
		return
	}
	if cur == v.lastToken.Load() {
		return
	}
	v.converge(ctx)
}

// converge brings the View up to the backend's current token under refreshMu,
// which serialises refreshes so concurrent stale reads collapse onto one. It
// prefers the incremental path — upsert just the changed manifests a
// DeltaSource reports — and falls back to a full re-derive when no DeltaSource
// is wired, the delta is gapped (history pruned past the cursor, which any
// hard delete forces), or the delta pull errors. The full re-derive is the
// correctness backstop; the delta path is the optimisation over it.
func (v *View) converge(ctx context.Context) {
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()

	// Re-read inside the lock: a goroutine that won the race may already have
	// converged past the value our caller saw.
	from := v.lastToken.Load()
	target, err := v.tokenSrc.Token(ctx)
	if err != nil {
		return
	}
	if target == from {
		return
	}

	if v.delta != nil {
		if d, derr := v.delta.Since(ctx, from); derr == nil && !d.Gapped {
			started := time.Now()
			nodes := v.applyDelta(ctx, d.Changes)
			v.lastToken.Store(d.Next)
			v.publish(event.EventViewRebuilt, event.RebuiltPayload{
				Duration:  time.Since(started),
				NodeCount: nodes,
			})
			return
		}
		// derr != nil or Gapped → fall through to the full re-derive.
	}

	v.rebuildLocked(ctx, target)
}

// applyDelta upserts each changed manifest into the live trees, reusing the
// same insert/remove primitives as Add/Move so by-path collision re-election
// stays correct. Ext is resolved outside the data lock (it is I/O); the whole
// batch is then applied under one write lock, so a read sees the delta whole,
// not half-applied. An update whose manifest now fails the filter is dropped.
// Deletions never reach here — they arrive as Gapped and route to a full
// re-derive. Returns the post-apply node count for the rebuilt event.
func (v *View) applyDelta(ctx context.Context, changes []domain.Manifest) int64 {
	type prepared struct {
		m    domain.Manifest
		keep bool
	}
	prep := make([]prepared, 0, len(changes))
	for _, m := range changes {
		keep := v.passesFilter(m)
		if keep {
			v.populateExt(ctx, &m)
		}
		prep = append(prep, prepared{m: m, keep: keep})
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	for _, p := range prep {
		if rec, exists := v.artifacts[p.m.ArtifactID]; exists {
			v.removeArtifactFromTrees(p.m.ArtifactID, rec)
		}
		if p.keep {
			v.indexArtifact(p.m, false)
		}
	}
	return v.Stats.TotalNodes
}

// rebuildLocked re-derives every tree from the source and swaps the fresh
// state in. The caller holds refreshMu and has resolved target. The expensive
// walk runs without the data lock; only the swap takes it, so readers block
// for the swap alone (INV-107-2). A walk failure leaves the current view in
// place — a later read retries.
func (v *View) rebuildLocked(ctx context.Context, target source.Token) {
	started := time.Now()
	shadow, err := v.buildShadow(ctx)
	if err != nil {
		return
	}

	v.mu.Lock()
	v.trees = shadow.trees
	v.artifacts = shadow.artifacts
	v.pathOwner = shadow.pathOwner
	v.pathLosers = shadow.pathLosers
	v.seenKeys = shadow.seenKeys
	v.Stats = shadow.Stats
	v.mu.Unlock()

	v.lastToken.Store(target)
	v.publish(event.EventViewRebuilt, event.RebuiltPayload{
		Duration:  time.Since(started),
		NodeCount: shadow.Stats.TotalNodes,
	})
}

// buildShadow walks the source into a detached View whose freshly built trees
// can be swapped into v. Identity (src, opts, defs) is shared; the shadow's
// bus is nil so re-deriving does not re-emit per-path collision events. No
// data lock is held during the walk — it is slow I/O.
func (v *View) buildShadow(ctx context.Context) (*View, error) {
	nv := &View{
		Source:    v.Source,
		CreatedAt: v.CreatedAt,
		src:       v.src,
		opts:      v.opts,
		defs:      v.defs,
		// bus intentionally nil: a rebuild is a re-derivation, not new writes.
	}
	nv.initTrees()
	if err := nv.backfill(ctx); err != nil {
		return nil, err
	}
	return nv, nil
}
