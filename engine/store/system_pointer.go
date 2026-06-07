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
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/driver"
	"scrinium.dev/errs"
)

// gatherNames lists pointer paths under namespace/pointers/<prefix>
// and returns the namespace-relative names.
//
// For system.config the flat pointer "config/current" is surfaced
// separately when the prefix admits it, since it does not live under
// pointers/.
func (ss *systemStore) gatherNames(ctx context.Context, namespace, prefix string) ([]string, error) {
	// Pointer names are nested ("scrub/cursor", "config/v1"), so the
	// listing must recurse. drv.List is one level deep — fine for a
	// prefix that already resolves to a leaf directory, but it drops
	// nested names under a shallow or empty prefix. Walk the pointers/
	// subtree recursively instead: ListObjectsWithModTime reports
	// files only (never directories) and treats a missing prefix as an
	// empty walk.
	pointersRoot := namespace + "/pointers/"
	listPath := pointersRoot
	if prefix != "" {
		listPath += prefix
	}

	var out []string
	err := ss.drv.ListObjectsWithModTime(ctx, listPath, time.Time{}, func(o driver.ObjectMeta) error {
		if name := strings.TrimPrefix(o.Path, pointersRoot); name != "" {
			out = append(out, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("system store: list %q: %w", listPath, err)
	}

	// config/current keeps a flat path outside pointers/ (its on-disk
	// format predates the subtree), so the recursive walk never sees
	// it — surface it explicitly when the prefix would match.
	if namespace == domain.NamespaceSystemConfig &&
		(prefix == "" || prefix == "config" || prefix == "config/" || strings.HasPrefix("config/current", prefix)) {
		if _, err := ss.drv.Stat(ctx, namespace+"/current"); err == nil {
			out = append(out, "config/current")
		}
	}
	return out, nil
}

// readPointer reads the pointer file at the given driver path and
// parses its content as a ManifestDigest (system artifacts are
// addressed by their on-disk digest, not a floating handle). Returns
// errs.ErrArtifactNotFound when the pointer file is absent.
func (ss *systemStore) readPointer(ctx context.Context, ptrPath string) (domain.ManifestDigest, error) {
	digest, err := ss.readPointerAt(ctx, ptrPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errs.ErrArtifactNotFound
		}
		return "", err
	}
	return digest, nil
}

// readPointerAt surfaces os.ErrNotExist directly (Walk relies on it).
func (ss *systemStore) readPointerAt(ctx context.Context, ptrPath string) (domain.ManifestDigest, error) {
	rc, err := ss.drv.Get(ctx, ptrPath)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, maxSystemPointerSize))
	if err != nil {
		return "", fmt.Errorf("read pointer: %w", err)
	}
	digestStr := strings.TrimSpace(string(raw))
	if digestStr == "" {
		return "", errs.ErrCorruptedConfigPointer
	}
	if _, _, err := ss.hashes.Parse(digestStr); err != nil {
		return "", fmt.Errorf("%w: %v", errs.ErrCorruptedConfigPointer, err)
	}
	return domain.ManifestDigest(digestStr), nil
}

func (ss *systemStore) writePointer(ctx context.Context, ptrPath string, digest domain.ManifestDigest) error {
	body := []byte(string(digest) + "\n")
	return ss.drv.Put(ctx, ptrPath, bytes.NewReader(body))
}

// readArtifact returns a ReadHandle over the manifest's inline payload.
func (ss *systemStore) readArtifact(ctx context.Context, digest domain.ManifestDigest) (domain.ReadHandle, error) {
	m, err := ss.loadManifest(ctx, digest)
	if err != nil {
		return nil, err
	}
	if m.LayoutHeader.BlobStorage != domain.LayoutInline {
		return nil, fmt.Errorf("system store: expected inline layout for %s, got %q",
			digest, m.LayoutHeader.BlobStorage)
	}
	return ss.makeHandle(m), nil
}

func (ss *systemStore) loadManifest(ctx context.Context, digest domain.ManifestDigest) (domain.Manifest, error) {
	manifestPath, err := artifact.ManifestPath(digest)
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
	if err := artifact.VerifyManifestDigest(digest, fileBytes, ss.hashes); err != nil {
		return domain.Manifest{}, fmt.Errorf("verify manifest: %w", err)
	}
	m, err := artifact.Decode(fileBytes)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	// The digest is the physical name; the handle (if any) comes from the
	// body. System artifacts are handle-less — fall back to the digest as
	// ArtifactID so the index key matches what was written.
	m.Digest = digest
	if m.ArtifactID == "" {
		m.ArtifactID = domain.ArtifactID(digest)
	}
	return m, nil
}

// dropPredecessor removes the manifest file and index row of a
// superseded artifact. Best-effort: Orphan Scan reclaims survivors.
//
// Failures here are invisible to the caller — the pointer flip already
// returned success — so they are logged at Warn: the operation is
// logically complete but left an orphan for GC. No error is returned
// (that is the "best-effort" contract); the log is the only signal.
func (ss *systemStore) dropPredecessor(ctx context.Context, digest domain.ManifestDigest) {
	m, err := ss.loadManifest(ctx, digest)
	if err == nil {
		blobRefs := []string{string(m.BlobRef)}
		if delErr := ss.index.DeleteManifest(ctx, m.ArtifactID, blobRefs); delErr != nil {
			ss.logger().LogAttrs(ctx, slog.LevelWarn,
				"superseded artifact left in index (best-effort cleanup failed)",
				artifactIDAttr(m.ArtifactID), slog.String("error", delErr.Error()))
		}
	}
	if manifestPath, pErr := artifact.ManifestPath(digest); pErr == nil {
		if rmErr := ss.drv.Remove(ctx, manifestPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			ss.logger().LogAttrs(ctx, slog.LevelWarn,
				"superseded manifest file left on disk (best-effort cleanup failed)",
				artifactIDAttr(domain.ArtifactID(digest)), slog.String("error", rmErr.Error()))
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
