// Package runtime is the public handle composer hands back: a live,
// assembled Scrinium stack with a uniform lifecycle.
//
// This is the R10 seed of the fuller runtime described in
// 3. Reference (R11): the Store/Index/View/FSOps accessors and Close
// are real now; Run (the background-agent + surface loop) and the
// Wrapper/Agent/Surface lookups are stubs until R11 wires agents and
// surfaces and R12 lands the projection.Projection aggregate. The
// interface is additive — new accessors arrive without breaking the
// ones here.
package runtime

import (
	"context"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/projection"
	"scrinium.dev/engine/store"
)

// Runtime is an assembled, running Scrinium stack. Obtain one from
// composer.Load*; release it with Close.
type Runtime interface {
	// Store is the high-level CAS store (Put/Get/Delete/Walk + admin).
	Store() store.Store

	// Index is the metadata index backing the Store. Exposed for
	// diagnostics (extension listing, stats); most callers go
	// through Store.
	Index() index.StoreIndex

	// View is the read-side projection (trees by path/date/…).
	View() *projection.View

	// FSOps is the read/write filesystem facade surfaces wrap. Nil
	// when the runtime was assembled read-only without a projection.
	FSOps() *projection.FSOps

	// MountSession is the boot-unique identifier for this runtime.
	MountSession() domain.SessionID

	// Wrapper/Agent/Surface look up named components by the kind they
	// were registered under. Stubs until R11/R12; they return
	// errs.ErrNotImplemented today.
	Wrapper(name string) (any, error)
	Agent(name string) (any, error)
	Surface(name string) (any, error)

	// Run starts background agents and surfaces and blocks until ctx
	// is cancelled. Stub until R11 (returns nil immediately).
	Run(ctx context.Context) error

	// Close releases every resource the runtime owns (store, index,
	// view), idempotently.
	Close() error
}

// rt is the private concrete Runtime composer populates. Kept
// unexported per ADR-52: callers depend on the interface, not the
// struct, so the assembled shape can grow without an API break.
type rt struct {
	store        store.Store
	index        index.StoreIndex
	view         *projection.View
	fsops        *projection.FSOps
	mountSession domain.SessionID
	closeFn      func() error
}

var _ Runtime = (*rt)(nil)

// New builds a Runtime from already-assembled components. composer is
// the intended caller; closeFn unwinds the components in the correct
// order and must be idempotent.
func New(
	st store.Store,
	idx index.StoreIndex,
	view *projection.View,
	fsops *projection.FSOps,
	mountSession domain.SessionID,
	closeFn func() error,
) Runtime {
	return &rt{
		store:        st,
		index:        idx,
		view:         view,
		fsops:        fsops,
		mountSession: mountSession,
		closeFn:      closeFn,
	}
}

func (r *rt) Store() store.Store             { return r.store }
func (r *rt) Index() index.StoreIndex        { return r.index }
func (r *rt) View() *projection.View         { return r.view }
func (r *rt) FSOps() *projection.FSOps       { return r.fsops }
func (r *rt) MountSession() domain.SessionID { return r.mountSession }

func (r *rt) Wrapper(string) (any, error) { return nil, errs.ErrNotImplemented }
func (r *rt) Agent(string) (any, error)   { return nil, errs.ErrNotImplemented }
func (r *rt) Surface(string) (any, error) { return nil, errs.ErrNotImplemented }

func (r *rt) Run(ctx context.Context) error {
	// R11 wires the agent/surface loop here. For now there is nothing
	// to run: return immediately so callers can adopt the API shape.
	return nil
}

func (r *rt) Close() error {
	if r.closeFn == nil {
		return nil
	}
	return r.closeFn()
}
