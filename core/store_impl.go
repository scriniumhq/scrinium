package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/rkurbatov/scrinium/driver"
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
	activeConfig StoreConfig

	// State machine.
	stateMu     sync.RWMutex
	state       StoreState
	maintenance MaintenanceMode

	// Plugin registries — populated at construction; never mutated
	// after that.
	hashes       HashRegistry
	transformers TransformerRegistry
	keyResolver  KeyResolver

	// Capability token for system.* access. nil disables WalkSystem
	// only when authorisation enforcement is wired in M2+. M1.3
	// treats the token as opt-in metadata: presence does not yet
	// restrict, absence does not yet block.
	capabilityToken []byte
}

// State returns the current state of the Store. Cheap and
// lock-free for readers (RWMutex read).
func (s *store) State() StoreState {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.state
}

// Capabilities returns the underlying Driver's capability mask.
// Stable for the lifetime of the Store; not cached because the
// Driver is the source of truth and a future Driver may want to
// change its mask after a runtime probe.
func (s *store) Capabilities() driver.CapabilityMask {
	return s.drv.Capabilities()
}

// SetMaintenanceMode transitions the Store into the requested
// maintenance regime. Allowed transitions in M1.3 are: any → any.
//
// A transition into MaintenanceModeOffline blocks all subsequent
// operations except SetMaintenanceMode itself (back to None or
// ReadOnly) — that escape hatch is what the Offline doc-comment
// promises. We do not enforce it through a state-machine matrix
// here; the priority-of-checks in operation entry points covers
// it (each method checks ErrStoreOffline at its boundary).
//
// The transition is idempotent: setting the current mode again is
// a no-op success.
func (s *store) SetMaintenanceMode(ctx context.Context, mode MaintenanceMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch mode {
	case MaintenanceModeNone, MaintenanceModeReadOnly, MaintenanceModeOffline:
		// OK
	default:
		return fmt.Errorf("core.SetMaintenanceMode: invalid mode %d", mode)
	}

	s.stateMu.Lock()
	s.maintenance = mode
	s.stateMu.Unlock()

	// EventMaintenanceModeChanged is not in core/events.go yet;
	// when it lands (M3 alongside the GC / Scrub coordination
	// work) we will emit here. Logging-only would create surprise
	// state for the host; deliberate silence is the safer default.
	return nil
}

// maintenanceMode reads the current maintenance mode under the
// state lock. Used internally by methods that need to honour it
// (Walk, WalkSystem do not — they are read-only — but Capacity
// does, etc.).
func (s *store) maintenanceMode() MaintenanceMode {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.maintenance
}

// Capacity returns aggregated storage info. Best-effort in M1.3:
//
//   - ArtifactCount / BlobCount come from the Driver's
//     CountObjects on the conventional prefixes ("manifests",
//     "blobs"). This is a physical count of files, which agrees
//     with the index for healthy Stores; it diverges only during
//     pre-GC orphan windows and during recovery, both of which
//     are diagnostic situations where seeing the on-disk count is
//     exactly what the operator wants.
//   - TotalBytes / UsedBytes / AvailableBytes are -1 (sentinel
//     "unavailable"). Driver does not expose disk-free; precise
//     byte accounting requires a full scan we do not want to do
//     on Capacity. Real numbers arrive in M2 once StoreIndex
//     grows a sized-summary method.
//
// The method honours ctx cancellation between the two driver
// calls. Offline Stores reject Capacity (operators can still
// inspect through State / Capabilities).
func (s *store) Capacity(ctx context.Context) (StorageInfo, error) {
	if err := ctx.Err(); err != nil {
		return StorageInfo{}, err
	}
	if s.maintenanceMode() == MaintenanceModeOffline {
		return StorageInfo{}, ErrStoreOffline
	}

	out := StorageInfo{
		TotalBytes:     -1,
		UsedBytes:      -1,
		AvailableBytes: -1,
	}

	// Both prefixes may not exist on a fresh Store. A missing
	// prefix yields 0 from localfs (see meta.go), which is the
	// right answer for "no artifacts written yet".
	manifests, err := s.drv.CountObjects(ctx, "manifests")
	if err != nil {
		return StorageInfo{}, fmt.Errorf("core.Capacity: count manifests: %w", err)
	}
	out.ArtifactCount = manifests

	if err := ctx.Err(); err != nil {
		return StorageInfo{}, err
	}

	blobs, err := s.drv.CountObjects(ctx, "blobs")
	if err != nil {
		return StorageInfo{}, fmt.Errorf("core.Capacity: count blobs: %w", err)
	}
	out.BlobCount = blobs

	return out, nil
}

// Walk iterates over user manifests. See docs/4. API Reference/04
// §4.1 for the contract; this implementation enforces the
// namespace-syntax rules (reject system.* prefix, length limit)
// and delegates to the StoreIndex for the actual iteration.
//
// Pack manifests are excluded by the index (they live in
// packed_blobs, never in manifests). System namespaces are
// excluded by both the index ("*" wildcard skips system.*) and by
// us at the API surface (an explicit "system.foo" gets
// ErrReservedNamespace before the index sees it).
func (s *store) Walk(ctx context.Context, namespace string, cb func(Manifest) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkOperational(); err != nil {
		return err
	}
	if err := validateUserNamespace(namespace); err != nil {
		return err
	}
	return s.index.ListByNamespace(ctx, namespace, cb)
}

// WalkSystem iterates over manifests inside one of the four
// reserved system namespaces. See docs/4. API Reference/04 §4.2.
// Allowed namespaces: system.transit, system.manifests,
// system.state, system.config.
//
// Capability-token enforcement is opt-in by docs and TBD by
// implementation; M1.3 honours the namespace-syntax rules but
// does not yet block calls based on token contents. Tracking:
// 4. API Reference/01 §1.3.1 (WithCapabilityToken) and the
// related authorisation work in M2.
func (s *store) WalkSystem(ctx context.Context, namespace string, cb func(Manifest) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkOperational(); err != nil {
		return err
	}
	if !isSystemNamespace(namespace) {
		return ErrReservedNamespace
	}
	return s.index.ListByNamespace(ctx, namespace, cb)
}

// checkOperational returns the first sentinel that blocks read or
// write according to the priority-of-checks order documented in
// 2. Internals/01 Topology §1.4. M1.3 does not implement the
// Bootstrapping / Locked / Corrupted transitions yet (they arrive
// with the crypto pipeline in M2 and the descriptor consensus in
// M2.2), so this method handles Offline and ReadOnly only — for
// Capacity-style cheap reads. Mutating-only checks (ReadOnly
// blocks Put/Delete) live with those methods when they land in
// M1.4.
func (s *store) checkOperational() error {
	s.stateMu.RLock()
	state := s.state
	mode := s.maintenance
	s.stateMu.RUnlock()

	switch state {
	case StateCorrupted:
		return ErrStoreCorrupted
	case StateLocked:
		return ErrLocked
	case StateBootstrapping:
		return ErrStoreNotReady
	}
	if mode == MaintenanceModeOffline {
		return ErrStoreOffline
	}
	return nil
}

// validateUserNamespace enforces the contract of Walk's namespace
// argument. See docs §4.1.
func validateUserNamespace(ns string) error {
	if len(ns) > 255 {
		return ErrNamespaceTooLong
	}
	// "*" and "" are valid (wildcard / default namespace). Any
	// "system." prefix is reserved.
	if strings.HasPrefix(ns, "system.") {
		return ErrReservedNamespace
	}
	return nil
}

// isSystemNamespace reports whether the given string is one of the
// four reserved system namespace names. Wildcard ("*") and empty
// ("") are deliberately excluded — see docs §4.2 for the
// rationale.
func isSystemNamespace(ns string) bool {
	switch ns {
	case "system.transit",
		"system.manifests",
		"system.state",
		"system.config":
		return true
	}
	return false
}

// --- AdminStore: stubs implemented in later packs ---

func (s *store) Unlock(ctx context.Context) error {
	return errors.New("core.Store.Unlock: not implemented")
}

func (s *store) ExportRecoveryKit(ctx context.Context) ([]byte, error) {
	return nil, errors.New("core.Store.ExportRecoveryKit: not implemented")
}

func (s *store) RotateKEK(ctx context.Context) error {
	return errors.New("core.Store.RotateKEK: not implemented")
}

func (s *store) UpdateConfig(ctx context.Context, cfg StoreConfig) error {
	return errors.New("core.Store.UpdateConfig: not implemented")
}

func (s *store) ConfigHistory(ctx context.Context) ([]StoreConfig, error) {
	return nil, errors.New("core.Store.ConfigHistory: not implemented")
}

// --- DataStore: stubs implemented in M1.4 ---

func (s *store) Put(ctx context.Context, a Artifact, opts PutOptions) (ArtifactID, error) {
	return "", errors.New("core.Store.Put: not implemented")
}

func (s *store) PutBlob(ctx context.Context, r io.Reader, blobType BlobType) (ContentHash, error) {
	return "", errors.New("core.Store.PutBlob: not implemented")
}

func (s *store) Get(ctx context.Context, id ArtifactID, opts GetOptions) (ReadHandle, error) {
	return nil, errors.New("core.Store.Get: not implemented")
}

func (s *store) Delete(ctx context.Context, id ArtifactID) error {
	return errors.New("core.Store.Delete: not implemented")
}

func (s *store) Verify(ctx context.Context, id ArtifactID) error {
	return errors.New("core.Store.Verify: not implemented")
}

func (s *store) RollbackSession(ctx context.Context, sessionID string) error {
	return errors.New("core.Store.RollbackSession: not implemented")
}

// Compile-time interface conformance.
var _ Store = (*store)(nil)