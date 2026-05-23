package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/store/internal/blobpath"
	"scrinium.dev/engine/store/internal/manifestcodec"
)

// gatherNames lists pointer paths under namespace/pointers/<prefix>
// and returns the namespace-relative names.
//
// For system.config the flat pointer "config/current" is surfaced
// separately when the prefix admits it, since it does not live under
// pointers/.
func (ss *systemStore) gatherNames(ctx context.Context, namespace, prefix string) ([]string, error) {
	prefixPath := namespace + "/pointers/"
	if prefix != "" {
		prefixPath = namespace + "/pointers/" + prefix
	}
	paths, err := ss.drv.List(ctx, prefixPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("system store: list %q: %w", prefixPath, err)
	}
	out := make([]string, 0, len(paths))
	pointersPrefix := namespace + "/pointers/"
	for _, p := range paths {
		name := strings.TrimPrefix(p, pointersPrefix)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	if namespace == domain.NamespaceSystemConfig &&
		(prefix == "" || prefix == "config" || prefix == "config/" || strings.HasPrefix("config/current", prefix)) {
		if _, err := ss.drv.Stat(ctx, namespace+"/current"); err == nil {
			out = append(out, "config/current")
		}
	}
	return out, nil
}

// readPointer reads the pointer file at the given driver path and
// parses its content as an ArtifactID. Returns errs.ErrArtifactNotFound
// when the pointer file is absent.
func (ss *systemStore) readPointer(ctx context.Context, ptrPath string) (domain.ArtifactID, error) {
	id, err := ss.readPointerAt(ctx, ptrPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errs.ErrArtifactNotFound
		}
		return "", err
	}
	return id, nil
}

// readPointerAt surfaces os.ErrNotExist directly (Walk relies on it).
func (ss *systemStore) readPointerAt(ctx context.Context, ptrPath string) (domain.ArtifactID, error) {
	rc, err := ss.drv.Get(ctx, ptrPath)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxSystemPointerSize))
	if err != nil {
		return "", fmt.Errorf("read pointer: %w", err)
	}
	idStr := strings.TrimSpace(string(raw))
	if idStr == "" {
		return "", errs.ErrCorruptedConfigPointer
	}
	if _, _, err := ss.hashes.Parse(idStr); err != nil {
		return "", fmt.Errorf("%w: %v", errs.ErrCorruptedConfigPointer, err)
	}
	return domain.ArtifactID(idStr), nil
}

func (ss *systemStore) writePointer(ctx context.Context, ptrPath string, id domain.ArtifactID) error {
	body := []byte(string(id) + "\n")
	return ss.drv.Put(ctx, ptrPath, bytes.NewReader(body))
}

// readArtifact returns a ReadHandle over the manifest's inline payload.
func (ss *systemStore) readArtifact(ctx context.Context, id domain.ArtifactID) (ReadHandle, error) {
	m, err := ss.loadManifest(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.LayoutHeader.BlobStorage != domain.LayoutInline {
		return nil, fmt.Errorf("system store: expected inline layout for %s, got %q",
			id, m.LayoutHeader.BlobStorage)
	}
	return ss.makeHandle(m), nil
}

func (ss *systemStore) loadManifest(ctx context.Context, id domain.ArtifactID) (domain.Manifest, error) {
	manifestPath, err := blobpath.ManifestPath(id)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("manifest path: %w", err)
	}
	rc, err := ss.drv.Get(ctx, manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.Manifest{}, errs.ErrArtifactNotFound
		}
		return domain.Manifest{}, fmt.Errorf("get manifest: %w", err)
	}
	defer rc.Close()
	fileBytes, err := io.ReadAll(rc)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	if err := manifestcodec.VerifyArtifactID(id, fileBytes, ss.hashes); err != nil {
		return domain.Manifest{}, fmt.Errorf("verify manifest: %w", err)
	}
	m, err := manifestcodec.DecodeFile(fileBytes)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	m.ArtifactID = id
	return m, nil
}

// dropPredecessor removes the manifest file and index row of a
// superseded artifact. Best-effort: Orphan Scan reclaims survivors.
//
// Failures here are invisible to the caller — the pointer flip already
// returned success — so they are logged at Warn: the operation is
// logically complete but left an orphan for GC. No error is returned
// (that is the "best-effort" contract); the log is the only signal.
func (ss *systemStore) dropPredecessor(ctx context.Context, id domain.ArtifactID) {
	m, err := ss.loadManifest(ctx, id)
	if err == nil {
		blobRefs := []string{string(m.BlobRef)}
		if delErr := ss.index.DeleteManifest(ctx, id, blobRefs); delErr != nil {
			ss.logger().LogAttrs(ctx, slog.LevelWarn,
				"superseded artifact left in index (best-effort cleanup failed)",
				artifactIDAttr(id), slog.String("error", delErr.Error()))
		}
	}
	if manifestPath, pErr := blobpath.ManifestPath(id); pErr == nil {
		if rmErr := ss.drv.Remove(ctx, manifestPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			ss.logger().LogAttrs(ctx, slog.LevelWarn,
				"superseded manifest file left on disk (best-effort cleanup failed)",
				artifactIDAttr(id), slog.String("error", rmErr.Error()))
		}
	}
}

// logger returns the systemStore's logger, never nil. Mirrors the
// store-level nil-safety so call sites need no guard.
func (ss *systemStore) logger() *slog.Logger {
	if ss.log == nil {
		return discardLogger
	}
	return ss.log
}
