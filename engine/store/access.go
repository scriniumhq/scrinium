package store

import (
	"context"
	"os"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// checkWritable extends checkOperational with the ReadOnly check.
// Used at the entry of every mutating method; read-only operations
// (Walk, Capacity, Get) use checkOperational alone.
func (s *store) checkWritable() error {
	if err := s.checkOperational(); err != nil {
		return err
	}
	if s.maintenanceMode() == domain.MaintenanceModeReadOnly {
		return errs.ErrStoreReadOnly
	}
	return nil
}

// Entry-preamble contract:
//
// Every public Store method MUST start with one of three
// canonical preambles:
//
//   - enterRead  — read-path methods (Get, Walk, Verify, Capacity,
//                  ExportRecoveryKit). Reject if state is Locked.
//   - enterWrite — write-path methods (Put, Delete, RollbackSession,
//                  UpdateConfig, SetPassphrase, RotateKEK). Same as
//                  enterRead plus the ReadOnly maintenance check.
//   - enterAdmin — admin methods that legitimately run in Locked
//                  (Unlock — its purpose is to leave Locked).
//                  Same as enterRead minus the Locked check.
//
// All three uniformly handle: ctx cancellation, closed-store
// refusal (os.ErrClosed), corrupted refusal, offline refusal,
// bootstrapping refusal. They differ only in how they treat
// Locked and ReadOnly.
//
// The set of methods that do NOT start with one of these is
// intentionally limited to: State, Capabilities, Config (pure
// in-memory readers), SetMaintenanceMode (the very escape hatch
// that toggles the regime), and Close (the gate itself).
// Any new method outside that set should start with enterRead/
// enterWrite/enterAdmin — no exceptions, no clever locality.

// enterRead is the canonical entry-preamble for read-path methods
// (Get, Verify, Walk, Capacity, ConfigHistory, ExportRecoveryKit).
// Combines context cancellation with the priority-of-checks gate.
// Unlock uses enterAdmin instead, since Locked is its working state.
func (s *store) enterRead(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.checkOperational()
}

// enterWrite is the write-path counterpart: ctx + checkWritable
// (which itself adds the ReadOnly guard on top of checkOperational).
// Used by Put, Delete, RollbackSession, UpdateConfig, and the
// descriptor-mutating admin methods (SetPassphrase, RotateKEK).
// Those admin methods follow up with their own crypto-state checks
// after taking cryptoMu — enterWrite handles only the universal
// gate; specifics stay with each method.
func (s *store) enterWrite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.checkWritable()
}

// enterAdmin is the entry-preamble for admin methods that may
// legitimately run in StateLocked — Unlock is the canonical
// example, since its purpose is to leave Locked. Behaves like
// enterRead but treats Locked as acceptable; every other gate
// (closed / corrupted / offline / bootstrapping) still applies.
//
// Used only by Unlock today. ExportRecoveryKit, SetPassphrase,
// RotateKEK reject Locked themselves and so go through enterRead
// or enterWrite, which treat Locked as a refusal.
func (s *store) enterAdmin(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.stateMu.RLock()
	closed := s.closed
	state := s.state
	mode := s.maintenance
	s.stateMu.RUnlock()

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
	// Locked is intentionally NOT checked here — admin callers
	// (Unlock) are the means of leaving Locked.
	return nil
}

// currentState reads the state field under stateMu, the uniform reader
// used by State() and every gated method.
func (s *store) currentState() domain.StoreState {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.state
}

// maintenanceMode reads the current maintenance mode under the state
// lock, for the methods that must honour it.
func (s *store) maintenanceMode() domain.MaintenanceMode {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.maintenance
}

// checkOperational returns the first sentinel that blocks a read or
// write, in priority order. Closed comes first — once Close is called,
// no other state matters. ReadOnly + mutating-op is checked one layer
// up by checkWritable.
func (s *store) checkOperational() error {
	s.stateMu.RLock()
	closed := s.closed
	state := s.state
	mode := s.maintenance
	s.stateMu.RUnlock()

	// Priority order:
	//   0. Closed        — shut down; no other state is meaningful past Close.
	//   1. Corrupted     — API physically unreadable, overrides everything.
	//   2. Offline       — explicit administrative block, overrides crypto.
	//   3. Bootstrapping — initialisation in flight.
	//   4. Locked        — passphrase required (encrypted store only).
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
