package store

import "scrinium.dev/engine/artifact"

// ManifestKeyProvider returns the Store's key provider for decoding
// encrypted manifests read directly off the Driver. The rebuild agent
// reads manifests outside the normal Get path — the index it would resolve
// through is exactly what it is rebuilding — so it cannot reuse the Store's
// internal read decode and needs the key material on its own.
//
// It returns nil for an unencrypted Store (no resolver) or a non-*store
// implementation; the caller then falls back to the Plain-only decoder.
// The crypto.State accessor takes the crypto mutex and adapts the resolver,
// so this no longer reaches into crypto internals.
func ManifestKeyProvider(s Store) artifact.KeyProvider {
	concrete, ok := s.(*store)
	if !ok {
		return nil
	}
	return concrete.dataFacet.core.crypto.KeyProvider()
}
