package agent

import (
	"context"
	"fmt"
	"time"

	"scrinium.dev/engine/coreapi"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
)

// IngestMode is the operating mode of the Ingester.
type IngestMode string

const (
	// IngestModeOneShot — a single sweep of SourcePath; the agent
	// finishes once every found file has been processed.
	IngestModeOneShot IngestMode = "one-shot"

	// IngestModeWatch — continuous observation of SourcePath via
	// native OS mechanisms (fsnotify/inotify/FSEvents) when the
	// driver reports CapWatch; otherwise polling. Requires a
	// StateFile for resumable semantics.
	IngestModeWatch IngestMode = "watch"
)

// IngesterConfig is the configuration of a single Ingester. One
// instance — one external source.
type IngesterConfig struct {
	// SourcePath is the source's root directory or URI. Interpreted
	// by the driver.Driver passed to NewIngester.
	SourcePath string

	// Mode is OneShot or Watch.
	Mode IngestMode

	// BatchSize is the maximum number of files in a single flush.
	BatchSize int

	// FlushTimeout is the maximum waiting time for a batch in Watch
	// mode before a forced flush.
	FlushTimeout time.Duration

	// Concurrency is the number of parallel workers used for
	// hashing/transformation.
	Concurrency int

	// StateFile is the path to the cursor file. Required in Watch
	// mode.
	StateFile string
}

// Ingester is the background agent that captures data from an
// external source.
type Ingester interface {
	BackgroundAgent

	// ForceCommit immediately commits the accumulated batch
	// regardless of BatchSize/FlushTimeout. Used before an external
	// event (log rotation, snapshot, graceful shutdown).
	ForceCommit(ctx context.Context) error
}

// NewIngester creates an Ingester instance. User-managed: started
// by the host application explicitly. TODO(M6.3): fsnotify-driven ingestion.
//
// Returns errs.ErrIngesterNoState when cfg.Mode is Watch and no
// StateFile is set.
func NewIngester(
	source driver.Driver,
	target coreapi.DataStore,
	bus event.EventBus,
	cfg IngesterConfig,
) (Ingester, error) {
	if cfg.Mode == IngestModeWatch && cfg.StateFile == "" {
		return nil, errs.ErrIngesterNoState
	}
	return nil, fmt.Errorf("%w: agent.NewIngester", errs.ErrNotImplemented)
}
