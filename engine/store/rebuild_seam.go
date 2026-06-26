package store

import (
	"scrinium.dev/domain"
)

// rebuild_seam.go — the out-of-band access points package store exposes to
// the rebuild/checkpoint agent (engine/agent/rebuild). Both sit outside the
// normal Get/restore paths: the rebuild agent reads manifests straight off
// the Driver (the index it would resolve through is exactly what it is
// rebuilding), and checkpoint restore validates a checkpoint's provenance
// before it is trusted. Neither belongs on the Store interface — they are
// deliberate seams for one engine-internal consumer.

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
func ManifestKeyProvider(s Store) domain.KeyProvider {
	concrete, ok := s.(*store)
	if !ok {
		return nil
	}
	return concrete.crypto.KeyProvider()
}
