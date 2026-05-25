package artifactio

// read.go — the read half of the artifact I/O layer, mirroring the write
// half (Materialize/Assemble/Persist ↔ Load/OpenBlob/VerifyBlob). Pure
// mechanics over the injected Driver, StoreIndex, and registries plus the
// engine/artifact format; no *store back-reference, no event publishing
// (a verification failure is returned as ErrCorruptedBlob and the caller
// — store — decides whether to publish EventScrubFailed).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/errs"
)

// Load reads, verifies, and decodes the manifest file for id. It reads the
// file at artifact.ManifestPath, confirms ArtifactID == hash(file bytes)
// via artifact.VerifyArtifactID, then decodes (Plain bypasses the
// resolver; Sealed/Paranoid consult keys). A missing file is
// ErrArtifactNotFound; a hash mismatch is ErrCorruptedManifest.
//
// keys is the manifest key provider (store passes its KeyResolver adapted
// to artifact.KeyProvider; nil means "no resolver" — Plain decodes, an
// encrypted manifest surfaces ErrKeyNotFound).
func (x *IO) Load(ctx context.Context, id domain.ArtifactID, keys artifact.KeyProvider) (domain.Manifest, error) {
	if id == "" {
		return domain.Manifest{}, errs.ErrArtifactNotFound
	}
	manifestPath, err := artifact.ManifestPath(id)
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("artifactio.Load: path: %w", err)
	}
	rc, err := x.drv.Get(ctx, manifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return domain.Manifest{}, errs.ErrArtifactNotFound
		}
		return domain.Manifest{}, fmt.Errorf("artifactio.Load: read: %w", err)
	}
	raw, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		return domain.Manifest{}, fmt.Errorf("artifactio.Load: read body: %w", err)
	}
	if err := artifact.VerifyArtifactID(id, raw, x.hashes); err != nil {
		return domain.Manifest{}, err
	}
	m, err := artifact.DecodeEncrypted(raw, keys)
	if err != nil {
		return domain.Manifest{}, err
	}
	m.ArtifactID = id
	return m, nil
}

// OpenBlob returns a reader over the artifact's plaintext bytes: it opens
// the on-disk (or in-manifest) bytes and composes the inverse Pipeline
// (a no-op when the manifest has no stages). Closing the returned reader
// releases the underlying driver resource.
//
// Inline blobs are served from the manifest; Target blobs are resolved
// through the index (the read path follows where the blob was actually
// written, not what the current topology would compute) and opened
// through the Driver. LayoutExternalRef is not yet supported.
func (x *IO) OpenBlob(ctx context.Context, m domain.Manifest) (io.ReadCloser, error) {
	raw, err := x.openRawBlob(ctx, m)
	if err != nil {
		return nil, err
	}
	decoded, err := x.runner().BuildGet(m.Pipeline, raw)
	if err != nil {
		// BuildGet closed raw on its failure path.
		return nil, fmt.Errorf("artifactio.OpenBlob: build pipeline: %w", err)
	}
	return decoded, nil
}

// openRawBlob returns the on-disk (ciphertext-shaped) bytes without any
// pipeline decoding. Closing the returned reader releases driver-side
// resources; for Inline it is a no-op.
func (x *IO) openRawBlob(ctx context.Context, m domain.Manifest) (io.ReadCloser, error) {
	switch m.LayoutHeader.BlobStorage {
	case domain.LayoutInline:
		return io.NopCloser(bytes.NewReader(m.InlineBlob)), nil

	case domain.LayoutTarget:
		addr, err := x.index.Resolve(ctx, string(m.BlobRef))
		if err != nil {
			return nil, fmt.Errorf("artifactio: resolve blob path: %w", err)
		}
		rc, err := x.drv.Get(ctx, addr.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, errs.ErrCorruptedBlob
			}
			return nil, fmt.Errorf("artifactio: get blob: %w", err)
		}
		return rc, nil

	case domain.LayoutExternalRef:
		return nil, fmt.Errorf("%w: BlobStorage=ExternalRef awaits driver.Open URI dispatch", errs.ErrNotImplemented)

	default:
		return nil, fmt.Errorf("artifactio: unknown BlobStorage %q", m.LayoutHeader.BlobStorage)
	}
}

// VerifyBlob re-hashes the artifact's plaintext bytes and compares against
// manifest.ContentHash. The algorithm comes from the ContentHash prefix
// (not the current config), so historical artifacts still verify. Any
// decode-side failure inside the inverse pipeline (AEAD tag mismatch,
// decompressor error) is folded into ErrCorruptedBlob; a context error is
// returned as-is. The caller decides whether to publish EventScrubFailed.
func (x *IO) VerifyBlob(ctx context.Context, m domain.Manifest) error {
	_, want, hasher, err := artifact.ParseContentHash(x.hashes, m.ContentHash)
	if err != nil {
		return fmt.Errorf("artifactio.VerifyBlob: %w", err)
	}

	plaintext, err := x.OpenBlob(ctx, m)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(hasher, plaintext)
	closeErr := plaintext.Close()
	if copyErr != nil {
		if errors.Is(copyErr, context.Canceled) || errors.Is(copyErr, context.DeadlineExceeded) {
			return copyErr
		}
		return errors.Join(errs.ErrCorruptedBlob, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("artifactio.VerifyBlob: close blob reader: %w", closeErr)
	}
	if !bytes.Equal(hasher.Sum(nil), want) {
		return errs.ErrCorruptedBlob
	}
	return nil
}
