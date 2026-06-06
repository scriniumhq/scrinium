package store

import (
	"context"
	"fmt"

	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store/internal/descriptor"
)

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
