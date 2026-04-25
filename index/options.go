package index

import (
	"time"

	"github.com/rkurbatov/scrinium/core"
)

// IndexOption is an option for the constructor of a StoreIndex
// implementation. It applies to sqlite.NewStore, postgres.New,
// NewMultistore, etc.
type IndexOption func(*indexOptions)

// indexOptions is the internal aggregate of options. Not exported.
// Concrete content is decided by the implementations (M1.2+).
type indexOptions struct {
	publisher core.Publisher
}

// WithIndexPublisher provides a Publisher for emitting metric
// events (write_latency, contention_error, size). When omitted,
// events are silently dropped — the index's behaviour does not
// change.
func WithIndexPublisher(p core.Publisher) IndexOption {
	return func(o *indexOptions) { o.publisher = p }
}

// --- Metric events ---

// Metric event type constants emitted by StoreIndex and
// MultistoreIndex implementations.
const (
	EventIndexWriteLatency    = "index.write_latency"
	EventIndexContentionError = "index.contention_error"
	EventIndexSize            = "index.size"
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
