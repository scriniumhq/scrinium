// Package storeconfig owns the StoreConfig layer of the engine core:
// the pure defaults/validation functions over domain.StoreConfig and
// the persistence of the active config as the system.config artifact
// plus its system.config/current pointer.
//
// Split out of core so that config defaulting, immutable-parameter
// validation, and the system.config on-disk format live in one home
// rather than scattered across config_default.go, lifecycle.go and
// sysconfig.go. None of the functions here touch *store private
// state: the pure validators take a domain.StoreConfig, and the
// persistence functions take explicit dependencies (driver, hash
// registry, and a narrow ArtifactWriter the core satisfies).
//
// The package is core-internal for now (no external caller exists);
// the narrow ArtifactWriter interface keeps it decoupled enough that
// promoting it to engine/storeconfig later is mechanical, should a
// recovery tool or external inspector need the system.config format.
package storeconfig
