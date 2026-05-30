// Package storeconfig owns the StoreConfig layer: the pure
// defaults/validation functions over domain.StoreConfig and the
// persistence of the active config as the system.config artifact plus
// its system.config/current pointer.
//
// None of the functions touch *store private state — the validators
// take a domain.StoreConfig, and the persistence functions take
// explicit dependencies (driver, hash registry, and a narrow
// ArtifactWriter the store satisfies). The package is store-internal;
// the ArtifactWriter seam keeps it decoupled enough that promoting it
// to a public engine/storeconfig later is mechanical.
package storeconfig
