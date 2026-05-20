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
// Contracts (HostStorage, HostAdmin, TransitStore, QuarantineFilter,
// QuarantinedItem) and configuration types (HostStorageConfig,
// HostStorageStats, the policy enums) live in this package
// alongside the implementation: prior to ADR-53 they were split
// between curator/host and the parent curator package, which
// forced a host → curator cycle once decorators reached for
// TransitStore. Co-locating the surface and the values that flow
// through it keeps the package self-contained.
//
// TODO(M4.2): HostStorage transit and drain to remote.
package host
