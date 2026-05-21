package core

import (
	"context"

	"scrinium.dev/engine/core/internal/storeconfig"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
)

// maxSystemPointerSize caps how many bytes are read from a
// system-namespace pointer file ("<algo>-<hex>\n"). It guards
// readPointerAt in system_store_impl.go against a corrupted/huge
// pointer. Previously lived in sysconfig.go (removed in the config
// refactor); kept in core because it is generic system-store
// plumbing, not part of the StoreConfig format (storeconfig has its
// own private maxPointerSize for the config pointer).
const maxSystemPointerSize = 256

// configArtifactWriter adapts the engine core's writeInlineSystemArtifact
// primitive to the narrow storeconfig.ArtifactWriter interface. It lets
// the storeconfig subpackage own the system.config FORMAT (StoreConfig
// serialisation + pointer) while the core retains the MECHANICS of
// writing an inline system artifact — the same primitive system.state
// agents use.
//
// Constructed from (drv, idx, hashes) rather than from *store, because
// the config write path runs both before a *store exists (InitStore via
// buildStore) and on a live *store (UpdateConfig). The three deps are
// exactly what writeInlineSystemArtifact needs.
type configArtifactWriter struct {
	drv    driver.Driver
	idx    StoreIndex
	hashes domain.HashRegistry
}

func newConfigArtifactWriter(drv driver.Driver, idx StoreIndex, hashes domain.HashRegistry) storeconfig.ArtifactWriter {
	return configArtifactWriter{drv: drv, idx: idx, hashes: hashes}
}

// WriteInlineArtifact satisfies storeconfig.ArtifactWriter.
func (w configArtifactWriter) WriteInlineArtifact(
	ctx context.Context,
	namespace string,
	sessionID domain.SessionID,
	payload []byte,
	hashAlgo string,
) (domain.ArtifactID, error) {
	return writeInlineSystemArtifact(ctx, w.drv, w.idx, w.hashes, namespace, sessionID, payload, hashAlgo)
}
