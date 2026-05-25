package store

// Test-only exports of package-private symbols. Lives in
// export_test.go so the alias surface is invisible outside test
// builds. Future system writers (M3 Scrub/Snapshot/Maintenance)
// extend this file with their own write/read helper aliases.

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/orphanscan"
	"scrinium.dev/engine/store/internal/storeconfig"
)

// SysConfigPointer is the on-disk path of system.config/current.
const SysConfigPointer = domain.NamespaceSystemConfig + "/current"

// WriteSystemConfig is the test alias for writeSystemConfig.
func WriteSystemConfig(
	ctx context.Context,
	drv driver.Driver,
	idx index.StoreIndex,
	hashes domain.HashRegistry,
	cfg domain.StoreConfig,
) (domain.ArtifactID, error) {
	return storeconfig.Write(ctx, drv, configWriter(drv, idx, hashes), cfg)
}

// ReadSystemConfig is the test alias for readSystemConfig.
func ReadSystemConfig(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
) (domain.StoreConfig, error) {
	return storeconfig.Read(ctx, drv, hashes)
}

// StoreKeyResolver exposes the internal keyResolver field for
// tests so they can assert that promoteKeyResolverIfDefault
// did or did not run. Returns nil for non-*store implementers
// (e.g. test mocks) so the helper degrades cleanly.
func StoreKeyResolver(s Store) pipeline.KeyResolver {
	concrete, ok := s.(*store)
	if !ok {
		return nil
	}
	concrete.crypto.mu.Lock()
	defer concrete.crypto.mu.Unlock()
	return concrete.crypto.keyResolver
}

// ReadDriverFile reads a file from the Store's underlying Driver.
// Tests use this to inspect raw on-disk manifest bytes —
// in particular to verify that Sealed leaves system fields
// in plaintext while Paranoid hides them.
func ReadDriverFile(s Store, path string) ([]byte, error) {
	concrete, ok := s.(*store)
	if !ok {
		return nil, fmt.Errorf("ReadDriverFile: not a *store")
	}
	rc, err := concrete.drv.Get(context.Background(), path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// WriteDriverFile writes raw bytes to the Store's Driver, used
// by tests that need to inject tampered manifest contents to
// verify integrity-check paths. Bypasses Put — caller is
// responsible for the resulting on-disk consistency.
func WriteDriverFile(s Store, path string, data []byte) error {
	concrete, ok := s.(*store)
	if !ok {
		return fmt.Errorf("WriteDriverFile: not a *store")
	}
	return concrete.drv.Put(context.Background(), path, bytes.NewReader(data))
}

// RecoverOrphans is the test alias for the package-private
// recoverOrphans function. Used by recovery_faulty_test.go to
// drive the function with fake StoreIndex / faulty Driver values
// directly, bypassing the full Init/Open path.
func RecoverOrphans(ctx context.Context, drv driver.Driver, idx index.StoreIndex) (orphanscan.OrphanReport, error) {
	return orphanscan.RecoverOrphans(ctx, drv, idx)
}
