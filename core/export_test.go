package core

// Test-only exports of package-private symbols. Lives in
// export_test.go so the alias surface is invisible outside test
// builds. Future system writers (M3 Scrub/Snapshot/Maintenance)
// extend this file with their own write/read helper aliases.

import (
	"context"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver"
)

// SysConfigPointer is the on-disk path of system.config/current.
const SysConfigPointer = sysConfigPointer

// WriteSystemConfig is the test alias for writeSystemConfig.
func WriteSystemConfig(
	ctx context.Context,
	drv driver.Driver,
	idx StoreIndex,
	hashes domain.HashRegistry,
	cfg domain.StoreConfig,
) (domain.ArtifactID, error) {
	return writeSystemConfig(ctx, drv, idx, hashes, cfg)
}

// ReadSystemConfig is the test alias for readSystemConfig.
func ReadSystemConfig(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
) (domain.StoreConfig, error) {
	return readSystemConfig(ctx, drv, hashes)
}
