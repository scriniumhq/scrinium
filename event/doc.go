// Package event provides shared event types and the event bus
// implementation used by all Scrinium layers.
//
// This is a leaf package in the dependency DAG: it does not import
// anything from the project except the standard library. Placing it
// here, rather than inside curator, allows the minimal stack
// (a single Store) to subscribe to and publish events.
//
// The default EventBus implementation is synchronous, panic-safe, and
// non-persistent. Custom implementations (asynchronous, buffered,
// filtering) are the host application's responsibility and plug in
// through the Publisher interface declared in store.
//
// # Reserved type-string namespaces
//
// Event.Type is a free-form string but the engine reserves four
// prefixes for its own emitters. Each prefix has a single owning
// package — that's where the constant set and the payload structs
// live. User code must not emit under these prefixes; pick a
// project-specific namespace ("acme.quota_monitor.tripped") instead.
//
//	"store.*"      — event/store_payloads.go (Store-level: manifest_saved,
//	                 artifact_deleted, store_degraded, ...)
//	"agent.*"      — engine/agent/events.go (background-agent lifecycle:
//	                 started, progress, cycle, failed, ...)
//	"multistore.*" — engine/wrapper/multistore (orchestration:
//	                 backup_unavailable, replication_lag, ...; wired in M4)
//	"index.*"      — engine/index/events.go (StoreIndex metrics:
//	                 write_latency, contention_error, size)
//
// Reservations are by convention — the bus does not enforce them at
// runtime. Treat unknown user prefixes as opaque and forward.
package event
