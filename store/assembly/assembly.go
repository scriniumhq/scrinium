// Package assembly is the handle composer hands back: a live, fully
// assembled Scrinium stack — store, index, projection view, and the
// read/write FSOps facade — ready to use.
//
// It deliberately has no Serve/Run loop and no notion of a "surface".
// composer's job ends at assembling the stack; how it is exposed
// (mounted via FUSE, served over WebDAV, browsed over HTTP) is the
// concern of the application that holds the Assembly. An app reads the
// accessors it needs and drives its own server or mount with its own
// lifecycle. This keeps composer focused on "what is stored and how it
// is projected" and leaves "how it is served" to the app.
package assembly

import (
	"scrinium.dev/domain"
	"scrinium.dev/projection"
	"scrinium.dev/store/index"
	"scrinium.dev/store/store"
)

// Assembly is an assembled Scrinium stack. Obtain one from
// composer.Load*; release it with Close.
type Assembly interface {
	// Store is the high-level CAS store (Put/Get/Delete/Walk + admin).
	Store() store.Store

	// Index is the metadata index backing the Store. Exposed for
	// diagnostics; most callers go through Store.
	Index() index.StoreIndex

	// View is the read-side projection (trees by path/date/…).
	View() *projection.View

	// FSOps is the read/write filesystem facade. Nil when the
	// assembly was built without a projection section.
	FSOps() *projection.FSOps

	// MountSession is the boot-unique identifier for this assembly.
	MountSession() domain.SessionID

	// Info returns assembly metadata for diagnostics (the store URI,
	// namespace, editing policy, read-only flag). A cheap snapshot.
	Info() Info

	// Close releases the store, index, and view, idempotently.
	Close() error
}

// Info is assembly metadata an app may surface in diagnostics (e.g. a
// stats page). The assembly itself does not act on it.
type Info struct {
	StoreURI  string
	Namespace string
	Editing   string
	ReadOnly  bool
}

// asm is the private concrete Assembly composer populates. Unexported
// (ADR-52): callers depend on the interface, so the assembled shape
// can grow without an API break.
type asm struct {
	store        store.Store
	index        index.StoreIndex
	view         *projection.View
	fsops        *projection.FSOps
	mountSession domain.SessionID
	info         Info
	closeFn      func() error
}

var _ Assembly = (*asm)(nil)

// New builds an Assembly from already-assembled components. composer is
// the intended caller; closeFn unwinds store/index/view in the correct
// order and must be idempotent.
func New(
	st store.Store,
	idx index.StoreIndex,
	view *projection.View,
	fsops *projection.FSOps,
	mountSession domain.SessionID,
	info Info,
	closeFn func() error,
) Assembly {
	return &asm{
		store:        st,
		index:        idx,
		view:         view,
		fsops:        fsops,
		mountSession: mountSession,
		info:         info,
		closeFn:      closeFn,
	}
}

func (a *asm) Store() store.Store             { return a.store }
func (a *asm) Index() index.StoreIndex        { return a.index }
func (a *asm) View() *projection.View         { return a.view }
func (a *asm) FSOps() *projection.FSOps       { return a.fsops }
func (a *asm) MountSession() domain.SessionID { return a.mountSession }
func (a *asm) Info() Info                     { return a.info }

func (a *asm) Close() error {
	if a.closeFn == nil {
		return nil
	}
	return a.closeFn()
}
