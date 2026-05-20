// Package curator is the orchestrator at layer L3. It unites
// several Stores under a single management surface: routing
// (cf. wrapper/multistore), transit buffering (wrapper/host),
// cross-store deduplication, transparent decorators
// (wrapper/bundler, wrapper/chunker), and background services
// (Scrub, Snapshot).
//
// Curator is optional: the minimal stack (Driver + Store +
// StoreIndex) works without it. Curator implements core.DataStore —
// the user-facing artifact API; administrative operations (Unlock,
// RotateKEK, SetMaintenanceMode) do not exist at the Curator level
// and are performed per-store via Curator.Store(id).
//
// Layout per ADR-53:
//
//   - engine/curator (this package) — Curator interface, options,
//     events. Becomes the standalone-service stub when the network
//     surface lands in M6.
//   - engine/wrapper/multistore — multi-store wrapping, routing
//     types, MultistoreIndex, WrapperFactory/WrapperDeps.
//   - engine/wrapper/host — HostStorage and its policy types.
//   - engine/wrapper/bundler — small-blob packing decorator.
//   - engine/wrapper/chunker — CDC chunker decorator.
//
// DAG: curator imports core, driver, event, wrapper/multistore,
// wrapper/host. Wrappers never import curator.
package curator
