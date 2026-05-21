package store

import (
	"context"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/store/internal/storeconfig"
)

// maxSystemPointerSize caps how many bytes are read from a
// system-namespace pointer file ("<algo>-<hex>\n"). It guards
// readPointerAt in system_store_impl.go against a corrupted/huge
// pointer. Previously lived in sysconfig.go (removed in the config
// refactor); kept in core because it is generic system-store
// plumbing, not part of the StoreConfig format (storeconfig has its
// own private maxPointerSize for the config pointer).
const maxSystemPointerSize = 256

// configWriter returns a storeconfig.ArtifactWriter bound to the
// engine core's writeInlineSystemArtifact primitive. storeconfig owns
// the system.config FORMAT; core retains the MECHANICS of writing an
// inline system artifact (the same primitive system.state agents
// use). A closure rather than a named adapter type — the contract is
// one function, so a struct + method would be boilerplate.
//
// Built from (drv, idx, hashes) rather than from *store because the
// config write path runs both before a *store exists (InitStore) and
// on a live *store (UpdateConfig).
func configWriter(drv driver.Driver, idx StoreIndex, hashes domain.HashRegistry) storeconfig.ArtifactWriter {
	return func(
		ctx context.Context,
		namespace string,
		sessionID domain.SessionID,
		payload []byte,
		hashAlgo string,
	) (domain.ArtifactID, error) {
		return writeInlineSystemArtifact(ctx, drv, idx, hashes, namespace, sessionID, payload, hashAlgo)
	}
}
