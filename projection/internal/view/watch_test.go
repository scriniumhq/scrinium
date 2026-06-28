package view_test

import (
	"context"
	"testing"
	"time"

	vw "scrinium.dev/projection/internal/view"
	"scrinium.dev/testutil/projectionfx"

	"scrinium.dev/event"
)

// gatedWaiter releases Wait once per signal and returns the current fakeToken
// value; on ctx cancel it returns the error so Close can stop the watcher.
type gatedWaiter struct {
	tok  *fakeToken
	gate chan struct{}
}

func (w *gatedWaiter) Wait(ctx context.Context, after uint64) (uint64, error) {
	select {
	case <-w.gate:
		return w.tok.v.Load(), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (w *gatedWaiter) release() { w.gate <- struct{}{} }

// rebuildBus signals on every EventViewRebuilt without blocking the publisher.
type rebuildBus struct{ ch chan struct{} }

func (b *rebuildBus) Publish(e event.Event) {
	if e.Type == event.EventViewRebuilt {
		select {
		case b.ch <- struct{}{}:
		default:
		}
	}
}

func (b *rebuildBus) Subscribe(func(event.Event)) func() { return func() {} }

// TestWatcher_EagerRefreshWithoutRead checks the eager watcher re-derives the
// View when the backend advances, with no read to trigger it (ADR-107 7a).
func TestWatcher_EagerRefreshWithoutRead(t *testing.T) {
	ctx := context.Background()
	src := projectionfx.New()
	src.Add(makeManifest("art-a", "", 1, time.Now().UTC()), nil)

	tok := &fakeToken{}
	tok.v.Store(1)
	w := &gatedWaiter{tok: tok, gate: make(chan struct{}, 1)}
	bus := &rebuildBus{ch: make(chan struct{}, 4)}

	v, err := vw.New(ctx, src,
		vw.WithSyncSource(tok),
		vw.WithSyncWaiter(w),
		vw.WithEventBus(bus),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer v.Close()

	// Drain the initial rebuild emitted by New's backfill.
	select {
	case <-bus.ch:
	case <-time.After(time.Second):
		t.Fatal("no initial rebuild event")
	}

	// Another client writes and advances the token, then the waiter wakes.
	b := makeManifest("art-b", "", 1, time.Now().UTC())
	src.Add(b, nil)
	tok.v.Store(2)
	w.release()

	// The watcher rebuilds with no read on the View.
	select {
	case <-bus.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not rebuild after the backend advanced")
	}

	// And the foreign artifact is now present.
	if _, ok := v.LookupLocations(b.ArtifactID); !ok {
		t.Error("art-b not visible after watcher refresh")
	}
}

// TestWatcher_CloseStopsWatcher checks Close cancels the watcher and returns
// promptly (the Waiter respects ctx), rather than leaking the goroutine.
func TestWatcher_CloseStopsWatcher(t *testing.T) {
	ctx := context.Background()
	src := projectionfx.New()

	tok := &fakeToken{}
	w := &gatedWaiter{tok: tok, gate: make(chan struct{}, 1)}

	v, err := vw.New(ctx, src, vw.WithSyncSource(tok), vw.WithSyncWaiter(w))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	done := make(chan struct{})
	go func() { _ = v.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return — watcher goroutine not stopped")
	}
}
