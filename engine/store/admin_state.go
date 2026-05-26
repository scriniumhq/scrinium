package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/errs"
)

// State returns the current state of the Store. Cheap and
// lock-free for readers (RWMutex read).
func (a adminFacet) State() domain.StoreState { return a.currentState() }

// currentState reads the state field under stateMu. Lives on *core so
// both the adminFacet.State() accessor and the private impl helpers
// (unlockEncrypted, setPassphraseImpl, …) can read state uniformly.
func (c *core) currentState() domain.StoreState {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.state
}

// Capabilities returns the underlying Driver's capability mask.
// Stable for the lifetime of the Store; not cached because the
// Driver is the source of truth and a future Driver may want to
// change its mask after a runtime probe.
func (a adminFacet) Capabilities() driver.CapabilityMask {
	return a.drv.Capabilities()
}

// SetMaintenanceMode transitions the Store into the requested
// maintenance regime; any → any is allowed.
//
// A transition into MaintenanceModeOffline blocks all subsequent
// operations except SetMaintenanceMode itself (back to None or
// ReadOnly). That escape hatch is not enforced through a state-machine
// matrix here; the priority-of-checks in each operation's entry point
// covers it (every method checks ErrStoreOffline at its boundary).
//
// Idempotent: setting the current mode again is a no-op success.
func (a adminFacet) SetMaintenanceMode(ctx context.Context, mode domain.MaintenanceMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch mode {
	case domain.MaintenanceModeNone, domain.MaintenanceModeReadOnly, domain.MaintenanceModeOffline:
		// OK
	default:
		return fmt.Errorf("store.SetMaintenanceMode: invalid mode %d", mode)
	}

	a.stateMu.Lock()
	a.maintenance = mode
	a.stateMu.Unlock()

	// No event is emitted yet: a log-only signal would create surprise
	// state for the host, so deliberate silence is the safer default
	// until a proper MaintenanceModeChanged event exists. A Debug log is
	// not a host-visible event — safe to record the transition for
	// diagnostics. Lock-free: stateMu released above.
	a.componentLogger("store").LogAttrs(ctx, slog.LevelDebug, "maintenance mode set",
		storeIDAttr(a.core), maintenanceModeAttr(mode))
	return nil
}

// maintenanceMode reads the current maintenance mode under the state
// lock, for the methods that must honour it.
func (c *core) maintenanceMode() domain.MaintenanceMode {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.maintenance
}

// checkOperational returns the first sentinel that blocks a read or
// write, in priority order. Closed comes first — once Close is called,
// no other state matters. ReadOnly + mutating-op is checked one layer
// up by checkWritable.
func (c *core) checkOperational() error {
	c.stateMu.RLock()
	closed := c.closed
	state := c.state
	mode := c.maintenance
	c.stateMu.RUnlock()

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
