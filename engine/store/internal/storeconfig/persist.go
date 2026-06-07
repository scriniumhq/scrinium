package storeconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/errs"
)

const (
	namespace      = domain.NamespaceSystemConfig
	pointerPath    = domain.NamespaceSystemConfig + "/current"
	sessionID      = "init"
	maxPointerSize = 256 // sanity cap for "<algo>-<hex>\n"
)

// ArtifactWriter persists an inline system artifact and returns its
// ArtifactID. It is the single write primitive the config
// persistence needs from the engine core; core passes a closure over
// its writeInlineSystemArtifact primitive.
//
// A function type, not an interface: the contract is one method, and
// a named adapter struct in core would be pure boilerplate. core owns
// the MECHANICS of writing an inline artifact (manifest build,
// hashing, indexing) — shared with system.state agents — while
// storeconfig owns the system.config FORMAT (StoreConfig
// serialisation + pointer).
type ArtifactWriter func(
	ctx context.Context,
	namespace string,
	sessionID domain.SessionID,
	payload []byte,
	hashAlgo string,
) (domain.ManifestDigest, error)

// Write persists the StoreConfig as a system.config inline artifact
// and atomically updates the system.config/current pointer to its
// ArtifactID. Returns the new ArtifactID.
//
// Per §10.1.4 the pointer file is the single source of truth for the
// active StoreConfig; the descriptor (store.json) carries only
// identity and crypto Paranoid.
func Write(
	ctx context.Context,
	drv driver.Driver,
	w ArtifactWriter,
	cfg domain.StoreConfig,
) (domain.ManifestDigest, error) {
	payload, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", fmt.Errorf("system config: marshal: %w", err)
	}
	payload = append(payload, '\n')

	digest, err := w(
		ctx, namespace, sessionID, payload, string(cfg.ContentHasher),
	)
	if err != nil {
		return "", err
	}

	pointerBytes := []byte(string(digest) + "\n")
	if err := drv.Put(ctx, pointerPath, bytes.NewReader(pointerBytes)); err != nil {
		return "", fmt.Errorf("system config: put pointer: %w", err)
	}
	return digest, nil
}

// Read follows system.config/current to read the active StoreConfig.
// Returns errs.ErrMissingConfigPointer, errs.ErrCorruptedConfigPointer,
// or errs.ErrDanglingConfigPointer per the failure modes documented in
// §10.1.4.
func Read(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
) (domain.StoreConfig, error) {
	digest, err := ReadPointer(ctx, drv, hashes)
	if err != nil {
		return domain.StoreConfig{}, err
	}
	return LoadByID(ctx, drv, hashes, digest)
}

// LoadByID reads the system.config artifact by its ArtifactID and
// returns the decoded StoreConfig. Bypasses the system.config/current
// pointer — used by ConfigHistory, which has the IDs from WalkSystem
// already, and reused by Read on top of ReadPointer.
func LoadByID(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
	digest domain.ManifestDigest,
) (domain.StoreConfig, error) {
	manifestPath, err := artifact.ManifestPath(digest)
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

	if err := artifact.VerifyManifestDigest(digest, fileBytes, hashes); err != nil {
		return domain.StoreConfig{}, fmt.Errorf("system config: verify: %w", err)
	}
	manifest, err := artifact.Decode(fileBytes)
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

// ReadPointer parses the pointer file's content into ManifestDigest.
// Separated out so tests can probe each failure mode (missing /
// corrupted / dangling) independently.
func ReadPointer(
	ctx context.Context,
	drv driver.Driver,
	hashes domain.HashRegistry,
) (domain.ManifestDigest, error) {
	rc, err := drv.Get(ctx, pointerPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errs.ErrMissingConfigPointer
		}
		return "", fmt.Errorf("system config: get pointer: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, maxPointerSize))
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
	return domain.ManifestDigest(idStr), nil
}
