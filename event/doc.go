// Package event provides shared event types and the event bus
// implementation used by all Scrinium layers.
//
// This is a leaf package in the dependency DAG: it does not import
// anything from the project except domain and the standard library.
// Placing identity here, rather than in each emitting layer, lets the
// minimal stack (a single Store) subscribe to and publish every event
// — and mirrors the errs package, where the cross-cutting contract is
// owned by the contract, not the implementation that raises it.
//
// The default EventBus implementation is synchronous, panic-safe, and
// non-persistent. Custom implementations (asynchronous, buffered,
// filtering) are the host application's responsibility and plug in
// through the Publisher interface.
//
// # Reserved type-string namespaces
//
// Event.Type is a free-form string, but the engine reserves four
// prefixes for its own emitters. The constant set and the payload
// structs for all four live in this one package, split by subsystem
// into separate files; the string prefix is what keeps the namespaces
// distinct. User code must not emit under these prefixes; pick a
// project-specific namespace ("acme.quota_monitor.tripped") instead.
//
//	"store.*"      — store_payloads.go (core/storage facts:
//	                 manifest_saved, artifact_deleted, store_degraded,
//	                 pack_sealed, artifact_migrated, ...)
//	"agent.*"      — agent_payloads.go (agent lifecycle: started,
//	                 progress, cycle, completed, failed, ...)
//	"index.*"      — index_payloads.go (StoreIndex metrics:
//	                 write_latency, contention_error, size)
//	"projection.*" — projection_payloads.go (projection: path_collision,
//	                 view_rebuilt)
//
// Reservations are by convention — the bus does not enforce them at
// runtime. Treat unknown user prefixes as opaque and forward.
package event
