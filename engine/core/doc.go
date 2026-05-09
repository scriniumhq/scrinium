// Package core is the Scrinium Storage Engine (layer L2).
//
// A self-contained CAS engine: it accepts Artifacts, runs them
// through a configurable Pipeline, places them on a backend through
// a Driver, and keeps accounting in a StoreIndex. It operates on
// cryptographic identifiers (ContentHash, BlobRef, ArtifactID) and
// has no knowledge of business metadata (Metadata is an opaque
// json.RawMessage).
//
// The Store contract is split into three interfaces:
//   - DataStore — operations on artifacts (Put, Get, Delete, Walk,
//     and so on). The surface seen by client code, decorators, and
//     Curator.
//   - AdminStore — administrative API (Unlock, RotateKEK, UpdateConfig).
//     The surface seen by the Store's owner.
//   - Store — the union of the two. Returned by InitStore and
//     OpenStore.
//
// DAG: core imports event and driver. It does not import plugin,
// index, curator, agent, maintenance, or projection.
package core
