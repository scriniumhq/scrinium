package assembly

import (
	"scrinium.dev/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/projection"
)

// Assembly is an assembled Scrinium stack. The scrinium facade obtains
// one from Build/Load* and wraps it; release it with Close.
type Assembly interface {
	// Store is the high-level CAS store (Put/Get/Delete/Walk + admin).
	Store() store.Store

	// Extensions lists the index extensions registered on the backing
	// store index, for diagnostics (e.g. a stats page). Empty when the
	// index backend exposes none. This is the only index detail the
	// assembly surfaces: the raw StoreIndex (with its mutating
	// IndexManifest/DeletePacked/VacuumInto) stays internal.
	//
	// Note: the assembly deliberately exposes no raw Driver either.
	// Built-in maintenance/background agents receive Driver and
	// StoreIndex directly from the assembler at construction time
	// (engine-internal); they do not reach them through this surface,
	// and neither do hosts.
	Extensions() []index.ExtensionInfo

	// Projection is the read-side View plus the optional read/write
	// FSOps facade, bundled. Nil when the assembly was built without a
	// projection section.
	Projection() *projection.Projection

	// MountSession is the boot-unique identifier for this assembly.
	MountSession() domain.SessionID

	// Info returns assembly metadata for diagnostics (the store URI,
	// namespace, editing policy, read-only flag, fresh-create flag). A
	// cheap snapshot.
	Info() Info

	// RecoveryKit returns the recovery-kit bytes produced when this
	// assembly freshly initialised an encrypted store, and true. For a
	// store that was opened (not created) or is unencrypted it returns
	// (nil, false). The host MUST persist a returned kit out of band:
	// it is the only path back into the store if the passphrase is
	// lost. The same bytes are available later via Store().Admin()'s
	// ExportRecoveryKit.
	RecoveryKit() ([]byte, bool)

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
	// Created is true when this assembly freshly initialised the store
	// (Init, or OpenOrInit that fell through to Init) rather than
	// opening an existing one.
	Created bool
}

// asm is the private concrete Assembly the assembler populates.
// Unexported: callers depend on the interface, so the assembled shape
// can grow without an API break.
type asm struct {
	store        store.Store
	index        index.StoreIndex
	proj         *projection.Projection
	mountSession domain.SessionID
	info         Info
	recoveryKit  []byte
	closeFn      func() error
}

var _ Assembly = (*asm)(nil)

// New builds an Assembly from already-assembled components. The
// assembler is the intended caller; closeFn unwinds store/index/view in
// the correct order and must be idempotent. recoveryKit carries the
// init-time kit bytes (nil unless a fresh encrypted store was created).
func New(
	st store.Store,
	idx index.StoreIndex,
	proj *projection.Projection,
	mountSession domain.SessionID,
	info Info,
	recoveryKit []byte,
	closeFn func() error,
) Assembly {
	return &asm{
		store:        st,
		index:        idx,
		proj:         proj,
		mountSession: mountSession,
		info:         info,
		recoveryKit:  recoveryKit,
		closeFn:      closeFn,
	}
}

func (a *asm) Store() store.Store                 { return a.store }
func (a *asm) Projection() *projection.Projection { return a.proj }
func (a *asm) MountSession() domain.SessionID     { return a.mountSession }
func (a *asm) Info() Info                         { return a.info }

// Extensions reports the index extensions when the backend implements
// index.ExtensionLister, and nil otherwise. The raw index is held only
// internally (a.index) and never handed out.
func (a *asm) Extensions() []index.ExtensionInfo {
	if l, ok := a.index.(index.ExtensionLister); ok {
		return l.ListExtensions()
	}
	return nil
}

func (a *asm) RecoveryKit() ([]byte, bool) {
	if len(a.recoveryKit) == 0 {
		return nil, false
	}
	return a.recoveryKit, true
}

func (a *asm) Close() error {
	if a.closeFn == nil {
		return nil
	}
	return a.closeFn()
}
