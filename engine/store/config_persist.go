package store

import (
	"context"
	"encoding/json"
	"fmt"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/internal/named"
	"scrinium.dev/errs"
)

// configName is the system-artifact name under which the active
// StoreConfig and its history live: store.config.<seq> (ADR-85). The
// active config is max(seq); a new config is published by claiming the
// next seq. There is no pointer file — the config path uses the same
// name→seq mechanism as every other system artifact (named).
const configName = "store.config"

// writeConfig persists cfg as a new store.config version and returns once the
// version is durably written. The active config becomes the one just
// written (max seq).
//
// This is the store.config PERSISTENCE: StoreConfig serialisation over
// named cells. named owns the cell mechanics (inline manifest build,
// seq claim, verify-on-read) shared with every other system artifact;
// the StoreConfig MODEL (defaults, validation) lives in package config.
func writeConfig(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
	cfg domain.StoreConfig,
) (uint64, error) {
	// KDFParams are input-only at InitStore: they are copied into the
	// descriptor body and live exclusively there (docs 11, KDFParams).
	// Never serialise them into the versioned store.config snapshots —
	// the config history is not a second home for KDF material. (R-a:
	// they used to leak into every snapshot.) cfg is a value copy, the
	// caller's struct is untouched.
	cfg.KDFParams = nil

	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("system config: marshal: %w", err)
	}
	payload = append(payload, '\n')

	fileBytes, _, err := named.BuildInlineManifest(configName, payload, string(cfg.ContentHasher), hashes, domain.ManifestCryptoPlain, nil, "")
	if err != nil {
		return 0, fmt.Errorf("system config: build: %w", err)
	}
	seq, _, err := named.ClaimVersion(ctx, drv, configName, fileBytes)
	if err != nil {
		return 0, fmt.Errorf("system config: write: %w", err)
	}
	return seq, nil
}

// readConfig returns the active StoreConfig (the highest store.config
// version). It bypasses the StoreIndex entirely — config must be
// readable at store-open before the index is trusted — by reading the
// version directory directly. Returns errs.ErrConfigMissing when
// no config has ever been written; a corrupted version surfaces as
// errs.ErrCorruptedContent from the verify-on-read in named.Load.
func readConfig(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
) (domain.StoreConfig, uint64, error) {
	seq, found, err := named.ResolveActiveSeq(ctx, drv, configName)
	if err != nil {
		return domain.StoreConfig{}, 0, fmt.Errorf("system config: resolve active: %w", err)
	}
	if !found {
		return domain.StoreConfig{}, 0, errs.ErrConfigMissing
	}
	cfg, err := loadVersion(ctx, drv, hashes, seq)
	if err != nil {
		return domain.StoreConfig{}, 0, err
	}
	return cfg, seq, nil
}

// activeConfigSeq resolves the max store.config version without decoding it
// — the cheap freshness probe of the liveness tick (ADR-110,
// INV-110-7): one readdir, no parse.
func activeConfigSeq(ctx context.Context, drv driver.Driver) (uint64, bool, error) {
	return named.ResolveActiveSeq(ctx, drv, configName)
}

// configHistory returns every store.config version decoded and defaulted,
// newest first (the active config is therefore element zero). Like Read,
// it enumerates the version directory rather than the index.
func configHistory(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
) ([]domain.StoreConfig, error) {
	seqs, err := named.ListVersions(ctx, drv, configName)
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
		out = append(out, config.ApplyDefaults(cfg))
	}
	return out, nil
}

// loadVersion reads, verifies, and unmarshals the config at a specific
// store.config seq.
func loadVersion(ctx context.Context, drv driver.Driver, hashes domain.HashRegistry, seq uint64) (domain.StoreConfig, error) {
	path, err := named.VersionPath(configName, seq)
	if err != nil {
		return domain.StoreConfig{}, err
	}
	m, err := named.Load(ctx, drv, hashes, path)
	if err != nil {
		return domain.StoreConfig{}, fmt.Errorf("system config: load: %w", err)
	}
	var cfg domain.StoreConfig
	if err := json.Unmarshal(m.InlineBlob, &cfg); err != nil {
		return domain.StoreConfig{}, fmt.Errorf("system config: unmarshal: %w", err)
	}
	return cfg, nil
}
