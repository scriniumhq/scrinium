package view

import (
	"context"
	"time"

	"scrinium.dev/event"
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
	v.rebuild(ctx)
}

// rebuild re-derives every tree from the source and swaps the fresh state in.
// refreshMu serialises rebuilds so concurrent stale reads collapse onto one
// walk. The expensive walk runs without the data lock; only the swap takes
// it, so readers block for the swap alone (INV-107-2: re-derive before
// returning). A walk failure leaves the current view in place — a later read
// retries.
func (v *View) rebuild(ctx context.Context) {
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()

	// Re-read inside the lock and target the latest token: a goroutine that
	// won the race may have already refreshed past the value our caller saw.
	target, err := v.tokenSrc.Token(ctx)
	if err != nil {
		return
	}
	if target == v.lastToken.Load() {
		return
	}

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
