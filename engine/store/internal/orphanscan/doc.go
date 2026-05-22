// Package orphanscan implements the bootstrap Orphan Scan: a forward
// sweep of the filesystem against the index that reclaims physical
// orphans left by crashed writes. Free functions over driver and
// coreapi.StoreIndex — no *store state — called once from the store
// bootstrap.
package orphanscan
