package store

import (
	"context"
	"fmt"
	"io"
	"sync"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/systemstore"
)

// store is the engine's internal implementation of Store. It is
// not exported: clients receive a Store interface from InitStore
// and OpenStore.
//
// Concurrency model: the state field is protected by stateMu. Most
// data-path methods (Put, Get, Delete) consult state at entry and
// proceed without holding the lock; long-running operations must
// not block administrative state transitions. AdminStore methods
// that mutate state (Unlock, SetMaintenanceMode) take the write
// lock for the duration of the transition.
//
// Lock ordering. When more than one of the three mutexes is taken
// in the same call path, they MUST be acquired in this order:
//
//	cryptoMu  →  stateMu  →  cfgMu
//
// Reverse acquisition (e.g. cfgMu → cryptoMu) is forbidden because
// it deadlocks against the forward path. snapshotConfig and
// maintenanceMode helpers take their lock in isolation and release
// it before returning, so callers free to acquire one of the other
// two afterwards — what is not allowed is holding cfgMu (or stateMu)
// and reaching for cryptoMu inside that scope.
//
// Current obeyors of the order:
//   - unlockEncrypted, setPassphraseImpl, rotateKEKImpl: cryptoMu
//     held for the operation, stateMu taken briefly inside for
//     the transition.
//   - Put: snapshotConfig (cfgMu) at the top, released; cryptoMu
//     taken later in Phase 2 — sequential, not nested.
//   - Get / loadManifest: cryptoMu only.
type store struct {
	// Identity and dependencies.
	storeID string
	drv     driver.Driver
	index   coreapi.StoreIndex
	pub     coreapi.Publisher

	// Configuration. activeConfig is the StoreConfig in effect for
	// new operations; it is replaced atomically by UpdateConfig.
	cfgMu        sync.RWMutex
	activeConfig domain.StoreConfig

	// State machine.
	stateMu     sync.RWMutex
	state       domain.StoreState
	maintenance domain.MaintenanceMode

	// Plugin registries — populated at construction; never mutated
	// after that.
	hashes       domain.HashRegistry
	transformers pipeline.TransformerRegistry
	keyResolver  pipeline.KeyResolver

	// SystemStore facade. Initialised once at construction; nil
	// only in unit tests that build a *store by hand without
	// going through the full constructor.
	system *systemstore.SystemStore

	// Crypto state. cryptoMu guards the trio (descriptor, dek,
	// passphraseProvider) because Unlock / SetPassphrase /
	// RotateKEK rewrite them together.
	//
	// descriptor holds the current on-disk descriptor, kept in
	// memory after bootstrap so RotateKEK and SetPassphrase can
	// produce a successor (Sequence + 1, fresh KDFParams) without
	// re-reading from the Driver.
	//
	// dek is the unwrapped data-encryption key. nil for Plain
	// Stores and for encrypted Stores in StateLocked. Populated
	// at successful Unlock; cleared (aead.Wipe + nil) when the
	// state machine returns to Locked.
	//
	// passphraseProvider is captured from WithPassphrase at
	// construction. Stays for the Store's lifetime so subsequent
	// AdminStore operations (RotateKEK after a sleep, etc.) can
	// re-prompt without the host application threading the
	// provider through every call.
	cryptoMu           sync.Mutex
	desc               *descriptor.Descriptor
	dek                []byte
	passphraseProvider PassphraseProvider

	// closed is set by Close. Guarded by stateMu. Reads from
	// non-Close paths use a fast no-op check; the canonical
	// "operational" gate is checkOperational, which compares
	// state/maintenance and is unaffected by closed (Close
	// transitions state to Locked anyway).
	closed bool
}

// AdminStore crypto methods — bodies live in core/crypto_admin.go.

func (s *store) Unlock(ctx context.Context) error {
	return s.unlockEncrypted(ctx)
}

func (s *store) ExportRecoveryKit(ctx context.Context) ([]byte, error) {
	return s.exportRecoveryKitImpl(ctx)
}

func (s *store) RotateKEK(ctx context.Context) error {
	return s.rotateKEKImpl(ctx)
}

func (s *store) SetPassphrase(ctx context.Context) error {
	return s.setPassphraseImpl(ctx)
}

// --- DataStore: stubs implemented in M1.4 ---

func (s *store) PutBlob(ctx context.Context, r io.Reader, blobType domain.BlobType) (domain.ContentHash, error) {
	// PutBlob is a level-3 decorator entry point used by
	// chunker.Wrapper (M5.2) to write anonymous chunks without
	// producing a manifest. Two pending changes converge here:
	//
	//   1. The implementation lands with chunker.Wrapper in M5.2.
	//   2. The method itself is moving off DataStore onto a
	//      separate BlobStore interface at the start of M5 — see
	//      docs 7. Planning/backlog.md "ADR-TBD: Вынос PutBlob в
	//      отдельный интерфейс BlobStore". Application code will
	//      no longer see PutBlob in DataStore autocomplete.
	//
	// Until then the stub here keeps the current DataStore
	// contract honest: callers who reach for PutBlob today get a
	// clear "not implemented" rather than silent success.
	return "", fmt.Errorf("%w: store.PutBlob is deferred to M5 (chunker.Wrapper); the method itself moves to BlobStore at M5 start (backlog ADR-TBD)",
		errs.ErrNotImplemented)
}

// Compile-time interface conformance.

// System returns the SystemStore facade. Part of the AdminStore
// interface.
func (s *store) System() coreapi.SystemStore { return s.system }

var _ coreapi.Store = (*store)(nil)
