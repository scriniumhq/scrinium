// Package recovery implements the bootstrap Orphan Scan (docs/2
// §10.2): a forward sweep of the filesystem against the index that
// reclaims physical orphans left by crashed writes. Free functions
// over driver + coreapi.StoreIndex — no *store state — so it lives
// in its own internal package, called once from the store bootstrap.
package recovery
