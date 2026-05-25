package agent

import (
	"context"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// --- Sync Agent (Reserved, D-05) ---

// SyncAgent is the background replicator of artifacts between a
// Target and a Backup Store.
//
// Status: Reserved. The interface is fixed for Curator API
// stability; the implementation is deferred until a separate
// decision on the Reconciliation mechanism (event-driven,
// pull/push, quarantine) is made.
type SyncAgent interface {
	BackgroundAgent

	// Trigger schedules an out-of-band synchronisation of the
	// given artifact between the Target and the Backup Store
	// outside the event queue.
	Trigger(ctx context.Context, artifactID domain.ArtifactID) error
}

// --- Ejector Agent ---

// EjectorConfig configures the Ejector.
type EjectorConfig struct {
	// TempDir is the directory for ejected files. Should reside
	// on the same filesystem as the Location to allow efficient
	// Clone (CoW).
	TempDir string

	// Concurrency is the worker-pool size.
	Concurrency int

	// QueueSize is the depth of the task queue. Eject returns
	// ErrEjectorQueueFull when the queue is full.
	QueueSize int
}

// Ejector materialises artifacts into the host OS environment on
// demand. User-managed: created by the host application explicitly.
// It uses a background worker pool for heavy I/O — this fits the
// BackgroundAgent model with Run for the pool and an Eject method
// that submits tasks.
type Ejector interface {
	BackgroundAgent

	// Eject schedules the materialisation of the given artifact
	// at targetPath. The method does not block on the physical
	// copy. It returns immediately with an error on a full queue or
	// a missing artifact. The execution result (success/failure)
	// is delivered through EventAgentCycle or EventAgentFailed.
	Eject(ctx context.Context, id domain.ArtifactID, targetPath string) error
}

// NewEjector creates an Ejector instance.
// TODO(M6.3): host-driven artifact ejection.
func NewEjector(
	source store.DataStore,
	bus event.EventBus,
	cfg EjectorConfig,
) (Ejector, error) {
	return nil, fmt.Errorf("%w: agent.NewEjector", errs.ErrNotImplemented)
}
