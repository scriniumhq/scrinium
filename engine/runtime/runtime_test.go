package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSurface records Serve/Close calls and can be told to fail.
type fakeSurface struct {
	name     string
	served   atomic.Bool
	closed   atomic.Bool
	failWith error
	// block: if true, Serve blocks until ctx is cancelled; otherwise it
	// returns failWith immediately.
	block bool
}

func (f *fakeSurface) Name() string { return f.name }

func (f *fakeSurface) Serve(ctx context.Context) error {
	f.served.Store(true)
	if !f.block {
		return f.failWith
	}
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeSurface) Close() error {
	f.closed.Store(true)
	return nil
}

func newRT(surfaces ...Surface) *rt {
	r, _ := New(nil, nil, nil, nil, "", Info{}, func(Runtime) ([]Surface, error) {
		return surfaces, nil
	}, nil)
	return r.(*rt)
}

func TestRunNoSurfacesBlocksUntilCancel(t *testing.T) {
	r := newRT()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-done:
		t.Fatal("Run returned before ctx cancel")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on clean cancel", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestRunServesAndClosesSurfaces(t *testing.T) {
	a := &fakeSurface{name: "a", block: true}
	b := &fakeSurface{name: "b", block: true}
	r := newRT(a, b)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)

	if !a.served.Load() || !b.served.Load() {
		t.Fatal("surfaces were not served")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run err = %v, want nil", err)
	}
	if !a.closed.Load() || !b.closed.Load() {
		t.Fatal("surfaces were not closed on shutdown")
	}
}

func TestRunSurfaceFailureTriggersShutdown(t *testing.T) {
	boom := errors.New("boom")
	a := &fakeSurface{name: "a", failWith: boom} // fails immediately
	b := &fakeSurface{name: "b", block: true}    // would block forever
	r := newRT(a, b)

	err := r.Run(context.Background())
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("Run err = %v, want wrapping %v", err, boom)
	}
	if !b.closed.Load() {
		t.Fatal("blocking surface was not closed after sibling failed")
	}
}

func TestSurfaceLookup(t *testing.T) {
	a := &fakeSurface{name: "fuse"}
	r := newRT(a)
	if got, err := r.Surface("fuse"); err != nil || got != a {
		t.Fatalf("Surface(fuse) = %v, %v", got, err)
	}
	if _, err := r.Surface("nope"); err == nil {
		t.Fatal("Surface(nope) should error")
	}
}
