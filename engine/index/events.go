package index

import "time"

// Metric events emitted by StoreIndex and MultistoreIndex
// implementations. Routed through the Publisher passed via
// WithIndexPublisher; without a publisher they are silently
// dropped — emission is a diagnostic surface, not a correctness
// requirement.
//
// All four event-type prefixes ("store.", "agent.", "curator.",
// "index.") are reserved per docs/2. Internals/01 §1.7. The
// "index." namespace covers any backend (sqlite, postgres,
// in-memory) — payload shapes are backend-agnostic.
const (
	// EventIndexWriteLatency — latency of one mutating method
	// (IndexManifest, DeleteManifest, ...). Emitted on
	// every successful and failing call so dashboards can compare
	// success/failure latency distributions.
	EventIndexWriteLatency = "index.write_latency"

	// EventIndexContentionError — contention condition observed
	// (SQLite SQLITE_BUSY past busy_timeout; equivalent in other
	// backends). The operation may still have succeeded after
	// retry; subscribers correlate with WriteLatency to tell the
	// difference.
	EventIndexContentionError = "index.contention_error"

	// EventIndexSize — periodic snapshot of index size. Emitted on
	// a backend-configurable interval; not a per-write event.
	EventIndexSize = "index.size"
)

// IndexWriteLatencyPayload is the latency of a single mutating
// operation. Operation is the method name (for example,
// "IndexManifest", "DeleteManifest").
type IndexWriteLatencyPayload struct {
	Operation string
	Duration  time.Duration
}

// IndexContentionErrorPayload is a write-contention event (for
// example, SQLITE_BUSY past busy_timeout). WaitedFor is how long
// the call waited before failing or succeeding.
type IndexContentionErrorPayload struct {
	Operation string
	WaitedFor time.Duration
}

// IndexSizePayload is a periodic snapshot of the index size.
// Emitted by the implementation at a configurable interval.
type IndexSizePayload struct {
	DBBytes       int64
	WALBytes      int64
	BlobCount     int64
	ManifestCount int64
}
