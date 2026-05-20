package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/blobpath"
	"scrinium.dev/engine/internal/manifestcodec"
)

const (
	sysConfigNamespace   = domain.NamespaceSystemConfig
	sysConfigPointer     = domain.NamespaceSystemConfig + "/current"
	sysConfigSessionID   = "init"
	maxConfigPointerSize = 256 // sanity cap for "<algo>-<hex>\n"
)

// writeSystemConfig persists the StoreConfig as a system.config
// inline artifact and atomically updates the system.config/current
// pointer to its ArtifactID. Returns the new ArtifactID.
//
// Per §10.1.4 the pointer file is the single source of truth for
// the active StoreConfig; the descriptor (store.json) carries only
// identity and crypto Paranoid.
func writeSystemConfig(
	ctx context.Context,
	drv driver.Driver,
	idx StoreIndex,
	hashes domain.HashRegistry,
	cfg domain.StoreConfig,
) (domain.ArtifactID, error) {
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("system config: marshal: %w", err)
	}
	payload = append(payload, '\n')

	id, err := writeInlineSystemArtifact(
		ctx, drv, idx, hashes,
		sysConfigNamespace, sysConfigSessionID, payload,
		string(cfg.ContentHasher),
	)
	if err != nil {
		return "", err
	}

	pointerBytes := []byte(string(id) + "\n")
	if err := drv.Put(ctx, sysConfigPointer, bytes.NewReader(pointerBytes)); err != nil {
		return "", fmt.Errorf("system config: put pointer: %w", err)
	}
	return id, nil
}

// readSystemConfig follows system.config/current to read the
// active StoreConfig. Returns errs.ErrMissingConfigPointer,
// errs.ErrCorruptedConfigPointer, or errs.ErrDanglingConfigPointer
// per the failure modes documented in §10.1.4.
func readSystemConfig(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
) (domain.StoreConfig, error) {
	id, err := readSystemConfigPointer(ctx, drv, hashes)
	if err != nil {
		return domain.StoreConfig{}, err
	}
	return loadSystemConfigByID(ctx, drv, hashes, id)
}

// loadSystemConfigByID reads the system.config artifact by its
// ArtifactID and returns the decoded StoreConfig. Bypasses the
// system.config/current pointer — used by ConfigHistory, which
// has the IDs from WalkSystem already, and reused by the active
// reader on top of readSystemConfigPointer.
func loadSystemConfigByID(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
	id domain.ArtifactID,
) (domain.StoreConfig, error) {
	manifestPath, err := blobpath.ManifestPath(id)
	if err != nil {
		return domain.StoreConfig{}, fmt.Errorf("%w: %v", errs.ErrCorruptedConfigPointer, err)
	}
	rc, err := drv.Get(ctx, manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.StoreConfig{}, errs.ErrDanglingConfigPointer
		}
		return domain.StoreConfig{}, fmt.Errorf("system config: get manifest: %w", err)
	}
	fileBytes, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return domain.StoreConfig{}, fmt.Errorf("system config: read manifest: %w", err)
	}

	if err := manifestcodec.VerifyArtifactID(id, fileBytes, hashes); err != nil {
		return domain.StoreConfig{}, fmt.Errorf("system config: verify: %w", err)
	}
	manifest, err := manifestcodec.DecodeFile(fileBytes)
	if err != nil {
		return domain.StoreConfig{}, fmt.Errorf("system config: decode: %w", err)
	}
	if manifest.LayoutHeader.BlobStorage != domain.LayoutInline {
		return domain.StoreConfig{}, fmt.Errorf(
			"system config: expected Inline layout, got %q",
			manifest.LayoutHeader.BlobStorage)
	}

	var cfg domain.StoreConfig
	if err := json.Unmarshal(manifest.InlineBlob, &cfg); err != nil {
		return domain.StoreConfig{}, fmt.Errorf("system config: unmarshal: %w", err)
	}
	return cfg, nil
}

// readSystemConfigPointer parses the pointer file's content into
// an ArtifactID. Separated out so tests can probe each failure
// mode (missing / corrupted / dangling) independently.
func readSystemConfigPointer(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
) (domain.ArtifactID, error) {
	rc, err := drv.Get(ctx, sysConfigPointer)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errs.ErrMissingConfigPointer
		}
		return "", fmt.Errorf("system config: get pointer: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, maxConfigPointerSize))
	_ = rc.Close()
	if err != nil {
		return "", fmt.Errorf("system config: read pointer: %w", err)
	}

	idStr := strings.TrimSpace(string(raw))
	if idStr == "" {
		return "", errs.ErrCorruptedConfigPointer
	}
	if _, _, err := hashes.Parse(idStr); err != nil {
		return "", fmt.Errorf("%w: %v", errs.ErrCorruptedConfigPointer, err)
	}
	return domain.ArtifactID(idStr), nil
}
