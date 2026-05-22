package store

import (
	"sync"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/systemstore"
)

// store is the engine's internal Store implementation. Clients
// receive a Store interface from InitStore / OpenStore; the concrete
// type is never exported. Its behaviour is split across sibling files
// by the interface each serves — data_*, admin_*, system_* — plus the
// lifecycle_/bootstrap_ construction files.
//
// Concurrency. state is guarded by stateMu; most data-path methods
// read it once at entry and proceed lock-free, so a long Put never
// blocks an admin state transition. Mutating admin methods hold the
// write lock for the transition only.
//
// Lock ordering — when more than one mutex is held in a call path,
// acquire in this order; the reverse deadlocks:
//
//	crypto.mu  →  stateMu  →  cfgMu
//
// snapshotConfig and maintenanceMode take their lock in isolation and
// release before returning, so a caller may take another lock after
// them; what is forbidden is holding cfgMu or stateMu and then
// reaching for crypto.mu.
type store struct {
	// Identity and dependencies.
	storeID string
	drv     driver.Driver
	index   coreapi.StoreIndex
	pub     coreapi.Publisher

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
	// unit tests that build a *store by hand.
	system *systemstore.SystemStore

	// crypto groups the DEK, descriptor, passphrase provider, and key
	// resolver with the mutex that guards them (crypto_state.go).
	crypto cryptoState
}

// System returns the SystemStore facade. Part of AdminStore; reached
// only through AdminStore, so DataStore consumers cannot see system
// state (ADR-57).
func (s *store) System() coreapi.SystemStore { return s.system }

var _ coreapi.Store = (*store)(nil)
