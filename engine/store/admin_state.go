package store

// state.go — store state machine accessors and the priority-of-checks
// gate (checkOperational). Mutating transitions live with the operations
// that drive them: Unlock in crypto_admin.go, Bootstrapping → Unlocked
// in lifecycle.go.

import (
	"context"
	"fmt"
	"os"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
)

// State returns the current state of the Store. Cheap and
// lock-free for readers (RWMutex read).
func (s *store) State() domain.StoreState {
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
// maintenance regime. Allowed transitions in M1.4 are: any → any.
//
// A transition into MaintenanceModeOffline blocks all subsequent
// operations except SetMaintenanceMode itself (back to None or
// ReadOnly) — that escape hatch is what the Offline doc-comment
// promises. We do not enforce it through a state-machine matrix
// here; the priority-of-checks in operation entry points covers
// it (each method checks errs.ErrStoreOffline at its boundary).
//
// The transition is idempotent: setting the current mode again is
// a no-op success.
func (s *store) SetMaintenanceMode(ctx context.Context, mode domain.MaintenanceMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch mode {
	case domain.MaintenanceModeNone, domain.MaintenanceModeReadOnly, domain.MaintenanceModeOffline:
		// OK
	default:
		return fmt.Errorf("store.SetMaintenanceMode: invalid mode %d", mode)
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
func (s *store) maintenanceMode() domain.MaintenanceMode {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.maintenance
}

// checkOperational returns the first sentinel that blocks read or
// write according to the priority-of-checks order documented in
// 2. Internals/01 Topology §1.4. Closed-store comes first — once
// Close is called, no other state matters.
//
// M1.4 does not implement the Bootstrapping / Corrupted transitions
// yet (they arrive with the descriptor consensus in M2.2). Locked
// is implemented for encrypted stores. ReadOnly + mutating-op is
// checked one layer up by checkWritable.
func (s *store) checkOperational() error {
	s.stateMu.RLock()
	closed := s.closed
	state := s.state
	mode := s.maintenance
	s.stateMu.RUnlock()

	// Priority order per docs/2. Internals/01 §1.4 "Check priority":
	//   0. Closed      — store has been shut down. Highest priority:
	//                    no other state is meaningful past Close.
	//   1. Corrupted   — API physically unreadable, overrides everything else.
	//   2. Offline     — explicit administrative block, overrides crypto.
	//   3. Bootstrapping — initialisation in flight.
	//   4. Locked      — passphrase required (encrypted store only).
	if closed {
		return os.ErrClosed
	}
	if state == domain.StateCorrupted {
		return errs.ErrStoreCorrupted
	}
	if mode == domain.MaintenanceModeOffline {
		return errs.ErrStoreOffline
	}
	if state == domain.StateBootstrapping {
		return errs.ErrStoreNotReady
	}
	if state == domain.StateLocked {
		return errs.ErrLocked
	}
	return nil
}
