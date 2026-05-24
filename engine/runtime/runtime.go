// Package runtime is the public handle composer hands back: a live,
// assembled Scrinium stack with a uniform lifecycle.
//
// R10 seeded the Store/Index/View/FSOps accessors and Close. R11 adds
// the Surface contract, the Run loop that drives configured surfaces,
// and the named lookups. The projection.Projection aggregate (R12)
// will later collapse View/FSOps/FSIndex behind a single accessor; the
// interface is additive, so that arrives without breaking callers.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/projection"
	"scrinium.dev/engine/store"
)

// Surface is an external access point onto a runtime — FUSE mount,
// WebDAV server, WebView HTTP server, gRPC endpoint, … A surface is
// constructed by its registered factory (bound to the runtime), then
// driven by Run.
//
// Serve blocks, serving requests until ctx is cancelled, and returns
// the reason it stopped (nil for a clean ctx-driven shutdown). Close
// releases the surface's resources (unmount, close listener); it is
// called by Run after Serve returns and must be safe to call once.
type Surface interface {
	// Name is the kind the surface was registered under ("fuse",
	// "webdav", …), used in logs and Runtime.Surface lookups.
	Name() string
	Serve(ctx context.Context) error
	Close() error
}

// Runtime is an assembled, running Scrinium stack. Obtain one from
// composer.Load*; release it with Close.
type Runtime interface {
	// Store is the high-level CAS store (Put/Get/Delete/Walk + admin).
	Store() store.Store

	// Index is the metadata index backing the Store. Exposed for
	// diagnostics; most callers go through Store.
	Index() index.StoreIndex

	// View is the read-side projection (trees by path/date/…).
	View() *projection.View

	// FSOps is the read/write filesystem facade surfaces wrap. Nil
	// when the runtime was assembled without a projection section.
	FSOps() *projection.FSOps

	// MountSession is the boot-unique identifier for this runtime.
	MountSession() domain.SessionID

	// Info returns assembly metadata surfaces use for diagnostics
	// (stats rendering): the store URI, namespace, editing policy,
	// read-only flag. It is a snapshot, cheap to call.
	Info() Info

	// Surface returns the configured surface registered under name, or
	// errs.ErrNotFound if there is none.
	Surface(name string) (Surface, error)

	// Wrapper/Agent look up named components. Stubs until they are
	// wired (M3/M4); they return errs.ErrNotImplemented today.
	Wrapper(name string) (any, error)
	Agent(name string) (any, error)

	// Run starts every configured surface and blocks until ctx is
	// cancelled or a surface fails, then shuts them all down. With no
	// surfaces it blocks until ctx is cancelled. Run does not close
	// the Store/Index/View — that is Close's job.
	Run(ctx context.Context) error

	// Close releases the store, index, and view, idempotently.
	Close() error
}

// Info is assembly metadata a runtime exposes for diagnostics. It is
// what surfaces render in their stats endpoint; the runtime itself
// does not act on it.
type Info struct {
	StoreURI  string
	Namespace string
	Editing   string
	ReadOnly  bool
}

// rt is the private concrete Runtime composer populates. Unexported
// (ADR-52): callers depend on the interface, so the assembled shape
// can grow without an API break.
type rt struct {
	store        store.Store
	index        index.StoreIndex
	view         *projection.View
	fsops        *projection.FSOps
	mountSession domain.SessionID
	info         Info
	surfaces     []Surface
	closeFn      func() error
}

var _ Runtime = (*rt)(nil)

// New builds a Runtime from already-assembled components. composer is
// the intended caller; closeFn unwinds store/index/view in the correct
// order and must be idempotent.
//
// buildSurfaces is invoked once with the freshly-constructed Runtime so
// surface factories can bind to it (a surface needs the runtime to
// reach Store/View/FSOps, and the runtime needs the surfaces to Run —
// New closes that loop). It may be nil for a surfaceless runtime. If it
// returns an error, New returns it and the runtime is not produced;
// closeFn is the caller's responsibility to run in that case.
func New(
	st store.Store,
	idx index.StoreIndex,
	view *projection.View,
	fsops *projection.FSOps,
	mountSession domain.SessionID,
	info Info,
	buildSurfaces func(Runtime) ([]Surface, error),
	closeFn func() error,
) (Runtime, error) {
	r := &rt{
		store:        st,
		index:        idx,
		view:         view,
		fsops:        fsops,
		mountSession: mountSession,
		info:         info,
		closeFn:      closeFn,
	}
	if buildSurfaces != nil {
		surfaces, err := buildSurfaces(r)
		if err != nil {
			return nil, err
		}
		r.surfaces = surfaces
	}
	return r, nil
}

func (r *rt) Store() store.Store             { return r.store }
func (r *rt) Index() index.StoreIndex        { return r.index }
func (r *rt) View() *projection.View         { return r.view }
func (r *rt) FSOps() *projection.FSOps       { return r.fsops }
func (r *rt) MountSession() domain.SessionID { return r.mountSession }
func (r *rt) Info() Info                     { return r.info }

func (r *rt) Surface(name string) (Surface, error) {
	for _, s := range r.surfaces {
		if s.Name() == name {
			return s, nil
		}
	}
	return nil, fmt.Errorf("runtime: no surface %q configured", name)
}

func (r *rt) Wrapper(string) (any, error) { return nil, errs.ErrNotImplemented }
func (r *rt) Agent(string) (any, error)   { return nil, errs.ErrNotImplemented }

// Run drives the configured surfaces. Each Serve runs in its own
// goroutine under a derived context; the first surface to return an
// error (or ctx cancellation) triggers shutdown of the rest. Every
// surface is Closed before Run returns. The returned error is the
// first non-context failure observed, or nil for a clean shutdown.
func (r *rt) Run(ctx context.Context) error {
	if len(r.surfaces) == 0 {
		<-ctx.Done()
		return nil
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, len(r.surfaces))
	for _, s := range r.surfaces {
		wg.Add(1)
		go func(s Surface) {
			defer wg.Done()
			if err := s.Serve(runCtx); err != nil && !errors.Is(err, context.Canceled) {
				errCh <- fmt.Errorf("surface %q: %w", s.Name(), err)
			}
		}(s)
	}

	// Wait for either ctx cancellation or the first surface failure,
	// then cancel the rest and let them drain.
	var firstErr error
	select {
	case <-ctx.Done():
	case firstErr = <-errCh:
	}
	cancel()
	wg.Wait()

	// Drain any further errors (best-effort: keep the first).
	close(errCh)
	for err := range errCh {
		if firstErr == nil {
			firstErr = err
		}
	}

	// Close all surfaces; keep the first close error if Serve was clean.
	for _, s := range r.surfaces {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("surface %q close: %w", s.Name(), err)
		}
	}
	return firstErr
}

func (r *rt) Close() error {
	if r.closeFn == nil {
		return nil
	}
	return r.closeFn()
}
