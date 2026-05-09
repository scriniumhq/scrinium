// Package host implements HostStorage — the transit buffer on a
// fast local disk used by Curator for deferred writes to slow
// Target Stores, manifest caching with ManifestStorage:
// Local/Replicated, and buffering before bundler packing.
//
// Configuration happens through curator.WithHostStorage.
// HostStorage is built by Curator internally from the supplied
// driver.Driver and HostStorageConfig — host-applications never
// instantiate this type directly.
//
// The contracts (HostStorage, HostAdmin, QuarantineFilter,
// QuarantinedItem) live in the parent curator package — see
// curator/host_contracts.go. This split avoids the cycle that
// would arise once this package's implementation imports curator
// for HostStorageStats and the surrounding wiring: contracts go
// up the DAG, implementation goes down.
//
// TODO(M4.2): HostStorage transit and drain to remote.
package host
