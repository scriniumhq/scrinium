package index

import (
	"scrinium.dev/engine/core"
)

// IndexOption is an option for the constructor of a StoreIndex
// implementation. It applies to sqlite.NewStore, postgres.New,
// NewMultistore, etc.
type IndexOption func(*IndexOptions)

// IndexOptions is the resolved option aggregate that index
// implementations apply at construction time. Exported because
// concrete backends (sqlite, postgres) live in subpackages and
// must read the resolved values to wire them into their own
// state.
//
// Add fields here only when they are common to every backend.
// Backend-specific knobs (busy_timeout for SQLite, pool size
// for Postgres) stay private to the implementing package.
type IndexOptions struct {
	// Publisher is the event bus to which the implementation
	// emits index.* metric events. nil disables emission.
	Publisher core.Publisher
}

// WithIndexPublisher provides a Publisher for emitting metric
// events (write_latency, contention_error, size). When omitted,
// events are silently dropped — the index's behaviour does not
// change.
func WithIndexPublisher(p core.Publisher) IndexOption {
	return func(o *IndexOptions) { o.Publisher = p }
}
