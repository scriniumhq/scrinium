package view

import "context"

// startWatcher launches the eager-refresh goroutine when a Waiter is wired
// (ADR-107): it blocks on Wait and re-derives the View as soon as another
// client advances the backend, instead of waiting for the next read to notice.
// No-op without a Waiter — the View then converges lazily on read. Called once
// from New, after the initial backfill.
func (v *View) startWatcher() {
	if v.waiter == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.watchCancel = cancel
	v.watchDone = make(chan struct{})
	go v.watchLoop(ctx)
}

// watchLoop blocks on Wait and refreshes on each wake until the context is
// cancelled (Close) or the Waiter gives up. refreshIfStale re-reads Token and
// rebuilds only if it actually moved, so a spurious wake costs one probe.
func (v *View) watchLoop(ctx context.Context) {
	defer close(v.watchDone)
	for {
		if _, err := v.waiter.Wait(ctx, v.lastToken.Load()); err != nil {
			return // ctx cancelled on Close, or the backend stopped waiting
		}
		if v.closed.Load() {
			return
		}
		v.refreshIfStale(ctx)
	}
}

// stopWatcher cancels the watcher and waits for it to exit. Safe when no
// watcher was started (nil cancel). Called from Close.
func (v *View) stopWatcher() {
	if v.watchCancel == nil {
		return
	}
	v.watchCancel()
	<-v.watchDone
}
