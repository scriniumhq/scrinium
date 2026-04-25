// Package curator is the orchestrator at layer L3. It unites
// several Stores under a single management surface: routing,
// transit buffering (HostStorage), cross-store deduplication,
// transparent decorators (bundler, chunker), and background
// services (Scrub, Snapshot).
//
// Curator is optional: the minimal stack (Driver + Store +
// StoreIndex) works without it. Curator implements core.DataStore —
// the user-facing artifact API; administrative operations (Unlock,
// RotateKEK, SetMaintenanceMode) do not exist at the Curator level
// and are performed per-store via Curator.Store(id).
//
// Subpackages:
//   - curator/bundler — small-blob packing decorator for .pack
//     volumes.
//   - curator/chunker — CDC chunker decorator.
//   - curator/host — internal HostStorage package. It has no public
//     API; configuration goes through curator.WithHostStorage.
//
// DAG: curator imports core, driver, event. It does not import
// agent, maintenance, or projection.
package curator
