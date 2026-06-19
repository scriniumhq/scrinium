package store

import (
	"context"
	"fmt"

	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store/internal/descriptor"
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
func ManifestKeyProvider(s Store) artifact.KeyProvider {
	concrete, ok := s.(*store)
	if !ok {
		return nil
	}
	return concrete.crypto.KeyProvider()
}

// VerifyCheckpointIdentity checks that the checkpoint at srcPath belongs to
// the Store identified by expectedStoreID, before it is restored into that
// Store's index. It reads the descriptor blob recorded in the checkpoint's
// store_meta and compares the StoreID it carries.
//
// This guards the case where a checkpoint reaches the restore path from
// outside the Store's own System() namespace (an import, a copied Location, a
// crossed mount). For checkpoints fetched from the Store's own namespace the
// identity matches by construction, so the check is cheap insurance.
//
// It returns nil (a pass) when there is nothing to contradict:
//   - expectedStoreID is empty (the caller opted out of the check), or
//   - idx does not implement index.CheckpointInspector (the backend cannot be
//     inspected; identity is then trusted by provenance), or
//   - the checkpoint records no descriptor blob (no identity to compare).
//
// A recorded identity that differs from expectedStoreID is an error: the
// checkpoint belongs to a different Store and must not be restored here.
func VerifyCheckpointIdentity(ctx context.Context, idx index.StoreIndex, srcPath, expectedStoreID string) error {
	if expectedStoreID == "" {
		return nil
	}
	insp, ok := idx.(index.CheckpointInspector)
	if !ok {
		return nil
	}
	blob, err := insp.CheckpointMeta(ctx, srcPath, descriptor.MetaKeyBlob)
	if err != nil {
		return fmt.Errorf("store: verify checkpoint identity: %w", err)
	}
	if blob == "" {
		return nil
	}
	d, err := descriptor.Unmarshal([]byte(blob))
	if err != nil {
		return fmt.Errorf("store: verify checkpoint identity: parse descriptor: %w", err)
	}
	if d.StoreID != expectedStoreID {
		return fmt.Errorf("store: checkpoint belongs to store %q, not %q", d.StoreID, expectedStoreID)
	}
	return nil
}
