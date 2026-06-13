package storeconfig

import (
	"context"
	"encoding/json"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/store/internal/systemlayout"
	"scrinium.dev/errs"
)

// configName is the system-artifact name under which the active
// StoreConfig and its history live: system/config/<seq> (ADR-85). The
// active config is max(seq); a new config is published by claiming the
// next seq. There is no pointer file — the config path uses the same
// name→seq mechanism as every other system artifact (systemlayout),
// which is what removed the bespoke pointer this package used to carry.
const configName = "config"

// Write persists cfg as a new system/config version and returns once the
// version is durably written. The active config becomes the one just
// written (max seq).
//
// storeconfig owns the system.config FORMAT (StoreConfig serialisation);
// systemlayout owns the MECHANICS (inline manifest build, seq claim,
// verify-on-read) shared with every other system artifact.
func Write(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
	cfg domain.StoreConfig,
) error {
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("system config: marshal: %w", err)
	}
	payload = append(payload, '\n')

	fileBytes, _, err := systemlayout.BuildInlineManifest(payload, string(cfg.ContentHasher), hashes)
	if err != nil {
		return fmt.Errorf("system config: build: %w", err)
	}
	if _, _, err := systemlayout.ClaimVersion(ctx, drv, configName, fileBytes); err != nil {
		return fmt.Errorf("system config: write: %w", err)
	}
	return nil
}

// Read returns the active StoreConfig (the highest system/config
// version). It bypasses the StoreIndex entirely — config must be
// readable at store-open before the index is trusted — by reading the
// version directory directly. Returns errs.ErrMissingConfigPointer when
// no config has ever been written; a corrupted version surfaces as
// errs.ErrCorruptedContent from the verify-on-read in systemlayout.Load.
func Read(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
) (domain.StoreConfig, error) {
	seq, found, err := systemlayout.ResolveActiveSeq(ctx, drv, configName)
	if err != nil {
		return domain.StoreConfig{}, fmt.Errorf("system config: resolve active: %w", err)
	}
	if !found {
		return domain.StoreConfig{}, errs.ErrMissingConfigPointer
	}
	return loadVersion(ctx, drv, hashes, seq)
}

// History returns every system/config version decoded and defaulted,
// newest first (the active config is therefore element zero). Like Read,
// it enumerates the version directory rather than the index.
func History(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
) ([]domain.StoreConfig, error) {
	seqs, err := systemlayout.ListVersions(ctx, drv, configName)
	if err != nil {
		return nil, fmt.Errorf("system config: list versions: %w", err)
	}
	out := make([]domain.StoreConfig, 0, len(seqs))
	// seqs is ascending; walk it in reverse so the active (max) version
	// comes first.
	for i := len(seqs) - 1; i >= 0; i-- {
		cfg, err := loadVersion(ctx, drv, hashes, seqs[i])
		if err != nil {
			return nil, err
		}
		out = append(out, ApplyDefaults(cfg))
	}
	return out, nil
}

// loadVersion reads, verifies, and unmarshals the config at a specific
// system/config seq.
func loadVersion(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry, seq uint64) (domain.StoreConfig, error) {
	path, err := systemlayout.VersionPath(configName, seq)
	if err != nil {
		return domain.StoreConfig{}, err
	}
	m, err := systemlayout.Load(ctx, drv, hashes, path)
	if err != nil {
		return domain.StoreConfig{}, fmt.Errorf("system config: load: %w", err)
	}
	var cfg domain.StoreConfig
	if err := json.Unmarshal(m.InlineBlob, &cfg); err != nil {
		return domain.StoreConfig{}, fmt.Errorf("system config: unmarshal: %w", err)
	}
	return cfg, nil
}
