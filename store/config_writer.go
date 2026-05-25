package store

import (
	"context"

	"scrinium.dev/domain"
	"scrinium.dev/store/driver"
	"scrinium.dev/store/index"
	"scrinium.dev/store/internal/storeconfig"
)

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
func configWriter(drv driver.Driver, idx index.StoreIndex, hashes domain.HashRegistry) storeconfig.ArtifactWriter {
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
