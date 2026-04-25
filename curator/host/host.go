// Package host implements HostStorage — the transit buffer on a
// fast local disk used by Curator for deferred writes to slow
// Target Stores, manifest caching with ManifestStorage:
// Local/Replicated, and buffering before bundler packing.
//
// This is an internal package: it has no public API. Configuration
// happens through curator.WithHostStorage. HostStorage is built by
// Curator internally from the supplied driver.Driver and
// HostStorageConfig.
//
// The TransitStore contract (for decorators via WrapperDeps) and
// HostAdmin (for Curator) are declared here for use within the
// curator/ package.
//
// Implementation lands in M4.2.
package host

import (
	"context"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/curator"
)

// HostStorage is the full contract of the transit store. Used
// internally by Curator.
type HostStorage interface {
	curator.TransitStore
	HostAdmin
}

// HostAdmin holds the administrative operations of HostStorage.
// They are accessible to Curator. Decorators see only TransitStore.
type HostAdmin interface {
	// Drain transfers files from system.transit to the Target
	// Stores. The route is computed by the Router at the moment of
	// transfer (DL-01).
	Drain(ctx context.Context) error

	// Stats returns a snapshot of the current transit state.
	Stats() curator.HostStorageStats

	// Recover restores HostStorage after a process crash: it
	// cleans up .tmp files, checks locks, and re-indexes the
	// transit area.
	Recover(ctx context.Context) error

	// Requeue moves a file (or files) from
	// system.transit/quarantine/ back into the active Drain queue.
	// The route will be recomputed during the next Drain (deferred
	// routing). When artifactID is nil, all files in quarantine
	// are returned.
	//
	// Returns the number of files actually moved. Files missing
	// from quarantine are silently skipped — the operation is
	// idempotent.
	Requeue(ctx context.Context, artifactID *core.ArtifactID) (int, error)

	// ListQuarantined returns a snapshot of the current quarantine
	// state. It does not block Drain. QuarantineFilter provides
	// pagination.
	ListQuarantined(ctx context.Context, filter QuarantineFilter) ([]QuarantinedItem, error)
}

// QuarantineFilter is the selection used by ListQuarantined.
type QuarantineFilter struct {
	// Namespace filters by namespace; an empty string means all.
	Namespace string

	// OlderThan limits results to files quarantined before the
	// given moment. The zero value means no filter.
	OlderThan time.Time

	// Limit caps the number of returned records. 0 means no limit.
	Limit int
}

// QuarantinedItem describes a single file in quarantine.
type QuarantinedItem struct {
	ArtifactID    core.ArtifactID
	BlobRef       string
	Namespace     string
	OriginalSize  int64
	QuarantinedAt time.Time
	Reason        string
}
