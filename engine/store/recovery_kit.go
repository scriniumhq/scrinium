package store

import (
	"context"
	"fmt"

	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/recoverykit"
)

// RecoveryKitRestoreInfo reports the outcome of
// RestoreDescriptorFromRecoveryKit.
type RecoveryKitRestoreInfo struct {
	// StoreID is the Store identity carried by the kit and written into
	// the rebuilt descriptor.
	StoreID string

	// DescriptorWritten is true once both descriptor replicas (L0 and
	// L1) have been persisted from the kit.
	DescriptorWritten bool
}

// RestoreDescriptorFromRecoveryKit rebuilds store.json (and its L1
// shadow .store.backup.json) from Recovery Kit bytes — the
// disaster-recovery path for when every on-disk descriptor replica is
// gone but the operator still holds the kit, the passphrase, and the
// encrypted blobs.
//
// It decodes and checksum-verifies the kit, maps its identity and
// crypto material onto a fresh descriptor (Sequence reset to 1, since no
// prior descriptor survives to carry one forward), and persists both
// replicas through drv. It performs no index or pointer work and unlocks
// nothing: the RebuildIndexAgent reconstructs the index from the on-disk
// manifests afterwards, and the host Unlocks with the passphrase as
// usual — the kit carries the same wrapped DEK and KDF parameters the
// original used, so the existing passphrase still opens the restored
// Store.
//
// A malformed or tampered kit returns errs.ErrRecoveryKitCorrupted
// (wrapped by recoverykit.Decode). The kit is always encrypted-Store
// material (a Plain Store has no kit), so the rebuilt descriptor is
// always DEKEncrypted.
func RestoreDescriptorFromRecoveryKit(ctx context.Context, drv driver.Driver, kit []byte) (RecoveryKitRestoreInfo, error) {
	if drv == nil {
		return RecoveryKitRestoreInfo{}, fmt.Errorf("store.RestoreDescriptorFromRecoveryKit: nil driver")
	}

	k, err := recoverykit.Decode(kit)
	if err != nil {
		// Decode already wraps errs.ErrRecoveryKitCorrupted with a
		// concrete reason; pass it through unaltered.
		return RecoveryKitRestoreInfo{}, err
	}

	desc := &descriptor.Descriptor{
		StoreID:       k.StoreID,
		SchemaVersion: descriptor.CurrentSchemaVersion,
		Sequence:      1,
		DEK:           k.EncryptedDEK,
		DEKEncrypted:  true,
		KDFParams: &descriptor.KDFParams{
			Algorithm: k.Algorithm,
			Time:      k.Time,
			Memory:    k.Memory,
			Threads:   k.Threads,
			Salt:      k.Salt,
		},
	}

	if err := descriptor.WriteBoth(ctx, drv, desc); err != nil {
		return RecoveryKitRestoreInfo{}, fmt.Errorf(
			"store.RestoreDescriptorFromRecoveryKit: persist descriptor: %w", err)
	}

	return RecoveryKitRestoreInfo{StoreID: k.StoreID, DescriptorWritten: true}, nil
}
