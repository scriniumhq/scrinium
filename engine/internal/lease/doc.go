// Package lease implements the Scrinium lease primitive: a short-lived,
// mutable, CAS-bypassing exclusive-access token stored as a single
// service file under system.state/<domain>/lease.
//
// A lease is NOT a CAS artifact. It is mutable (Renew rewrites the
// body), short-lived (TTL of tens of seconds to minutes), and is
// acquired before the Core is initialised (location.lock at OpenStore,
// before Pipeline/HashRegistry exist). It is therefore written
// directly through the Driver, bypassing the manifest/ref-count
// machinery.
//
// The package is Driver-only and path-agnostic on purpose: the caller
// supplies the full Driver path, so the same primitive serves all
// three leases without importing store or SystemStore —
//
//   - system.state/location.lock        (OpenStore, SQLite backend)
//   - system.state/gc/lease             (GC Agent, LeaderElection)
//   - system.state/maintenance/lease    (Rebuild / Migrate / Snapshot)
//
// Acquire / Renew / Release / Takeover follow §11.2. Concurrency safety
// without a coordinator rests on two mechanisms: an exclusive create
// (Driver.Put with WithExclusive, i.e. If-None-Match) wins the empty
// slot, and a per-hold nonce plus read-after-write verification settles
// races to take over an expired lease and detects a stale previous
// owner returning with a late Renew.
package lease
