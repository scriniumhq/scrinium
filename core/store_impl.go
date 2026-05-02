package core

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/rkurbatov/scrinium/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver"
	"github.com/rkurbatov/scrinium/errs"
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
type store struct {
	// Identity and dependencies.
	storeID string
	drv     driver.Driver
	index   StoreIndex
	pub     Publisher

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
	transformers TransformerRegistry
	keyResolver  KeyResolver

	// Capability token for system.* access. nil disables WalkSystem
	// only when authorisation enforcement is wired in M2+. M1.4
	// treats the token as opt-in metadata: presence does not yet
	// restrict, absence does not yet block.
	capabilityToken []byte

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
	// at successful Unlock; cleared (zeroBytes + nil) when the
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

func (s *store) UpdateConfig(ctx context.Context, cfg domain.StoreConfig) error {
	return fmt.Errorf("%w: core.Store.UpdateConfig", errs.ErrNotImplemented)
}

func (s *store) Config() domain.StoreConfig {
	return s.snapshotConfig()
}

func (s *store) ConfigHistory(ctx context.Context) ([]domain.StoreConfig, error) {
	return nil, fmt.Errorf("%w: core.Store.ConfigHistory", errs.ErrNotImplemented)
}

// --- DataStore: stubs implemented in M1.4 ---

func (s *store) PutBlob(ctx context.Context, r io.Reader, blobType domain.BlobType) (domain.ContentHash, error) {
	return "", fmt.Errorf("%w: core.Store.PutBlob", errs.ErrNotImplemented)
}

// Compile-time interface conformance.
var _ Store = (*store)(nil)
