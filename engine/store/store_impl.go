package store

import (
	"log/slog"
	"sync"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/crypto"
	"scrinium.dev/engine/systemstore"
	"scrinium.dev/event"
)

// core holds the entire internal state of a Store. It is never
// exported and never returned directly: clients receive a Store
// interface from InitStore / OpenStore, implemented by the *store
// wrapper below, whose three facets (data/admin/system) all share one
// *core. Splitting the surface into facets keeps the artifact-facing
// methods (dataFacet) free of the administrative ones (adminFacet)
// while both operate on the same underlying state.
//
// Behaviour is split across sibling files by the facet each serves —
// data_*, admin_*, system_* — plus the lifecycle_/bootstrap_
// construction files. The private helpers (enterRead/Write/Admin,
// withWriteDEK, loadManifest, snapshotConfig, publish, …) stay on
// *core, reachable by every facet.
//
// Concurrency. state is guarded by stateMu; most data-path methods
// read it once at entry and proceed lock-free, so a long Put never
// blocks an admin state transition. Mutating admin methods hold the
// write lock for the transition only.
//
// Lock ordering — stateMu and cfgMu are each taken in isolation and
// released before returning, so they are never nested. The crypto
// material has its own mutex inside crypto.State (engine/store/internal/
// crypto); its methods take and release it entirely on their own, and the
// store never holds stateMu or cfgMu while calling into crypto.State, so
// that mutex is never nested with these two either.
type core struct {
	// Identity and dependencies.
	storeID string
	drv     driver.Driver
	index   index.StoreIndex
	pub     event.Publisher

	// log is the diagnostic logger, namespaced to the "scrinium" group
	// at construction (ADR-60). Never read directly — go through
	// logger() / componentLogger(), which substitute a discard logger
	// when nil so call sites never need a guard.
	log *slog.Logger

	// activeConfig is the StoreConfig in effect for new operations,
	// replaced atomically by UpdateConfig under cfgMu.
	cfgMu        sync.RWMutex
	activeConfig domain.StoreConfig

	// State machine, guarded by stateMu.
	stateMu     sync.RWMutex
	state       domain.StoreState
	maintenance domain.MaintenanceMode
	closed      bool

	// Plugin registries — set at construction, never mutated after.
	hashes       domain.HashRegistry
	transformers pipeline.TransformerRegistry

	// SystemStore facade, wired once at construction. nil only in
	// unit tests that build a *core by hand.
	system systemstore.Store

	// crypto holds the DEK, descriptor, passphrase provider, and key
	// resolver behind its own mutex (engine/store/internal/crypto). Its
	// lifecycle methods take that mutex internally and release it before
	// the store touches stateMu, so the two are never held at once.
	crypto *crypto.State
}

// dataFacet is the artifact-facing facet (DataStore): Put, Get, Walk,
// and friends. Methods live in data_*.go.
type dataFacet struct{ *core }

// adminFacet is the administrative facet (AdminStore): State, Unlock,
// crypto rotation, Close, RunMaintenance, and the System() accessor.
// Methods live in admin_*.go, maintenance.go.
type adminFacet struct{ *core }

// store is the concrete Store handed to clients. It is a thin wrapper
// embedding the data and admin facets over one shared *core, so the
// flat Store = DataStore + AdminStore interface is satisfied by method
// promotion. systemFacet is NOT embedded here — it is reached through
// adminFacet.System(), which keeps the system Put/Get/Delete/Walk from
// colliding with the data ones of the same name.
type store struct {
	dataFacet
	adminFacet
}

var _ Store = (*store)(nil)

// newStore wraps a freshly built *core into the client-facing store.
func newStore(c *core) *store {
	return &store{
		dataFacet:  dataFacet{c},
		adminFacet: adminFacet{c},
	}
}

// System returns the SystemStore facade. Reached only through
// AdminStore, so DataStore consumers cannot see system state.
func (a adminFacet) System() systemstore.Store { return a.core.system }

// publish emits an event when a Publisher is configured. Cheap when
// nil — the common case for tests and minimal-stack hosts.
func (c *core) publish(typ string, payload any) {
	if c.pub == nil {
		return
	}
	c.pub.Publish(event.Event{Type: typ, Payload: payload})
}
