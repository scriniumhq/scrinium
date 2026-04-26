package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/blobpath"
)

// Verify performs a full integrity check of an artifact: re-reads
// the manifest (loadManifest already verifies its ArtifactID) and
// re-hashes the blob to confirm it matches manifest.ContentHash.
// On divergence emits EventScrubFailed and returns errs.ErrCorruptedBlob.
//
// M1.4 perimeter: BlobManifest only; Inline and Target layouts; no
// Pipeline. TOC, Pack, ExternalRef, and Pipeline transforms are
// deferred and return explicit errors.
func (s *store) Verify(ctx context.Context, id domain.ArtifactID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkOperational(); err != nil {
		return err
	}
	if id == "" {
		return errs.ErrArtifactNotFound
	}

	manifest, err := s.loadManifest(ctx, id)
	if err != nil {
		return err
	}

	switch manifest.Type {
	case domain.ManifestTypeBlob:
		// continue
	case domain.ManifestTypeTOC:
		return fmt.Errorf("core.Verify: ManifestTypeTOC deferred to M5")
	case domain.ManifestTypePack:
		// Engine-internal — invisible to clients (mirrors Get).
		return errs.ErrArtifactNotFound
	default:
		return fmt.Errorf("core.Verify: unknown manifest type %q", manifest.Type)
	}

	if len(manifest.Pipeline) > 0 {
		return fmt.Errorf("core.Verify: Pipeline transforms deferred to M2")
	}

	if err := s.verifyBlobHash(ctx, manifest); err != nil {
		s.publish(EventScrubFailed, ScrubFailedPayload{
			ArtifactID: id,
			Err:        err,
		})
		return err
	}
	return nil
}

// verifyBlobHash re-hashes blob bytes and compares against
// manifest.ContentHash. The algorithm is taken from the
// ContentHash prefix (not the current StoreConfig) so historical
// artifacts written under a previous hasher still verify.
func (s *store) verifyBlobHash(ctx context.Context, m domain.Manifest) error {
	algo, want, err := s.hashes.Parse(string(m.ContentHash))
	if err != nil {
		return fmt.Errorf("core.Verify: parse ContentHash: %w", err)
	}
	hasher, err := s.hashes.NewHasher(algo)
	if err != nil {
		return fmt.Errorf("core.Verify: hasher: %w", err)
	}

	switch m.LayoutHeader.BlobStorage {
	case "Inline":
		if _, err := hasher.Write(m.InlineBlob); err != nil {
			return fmt.Errorf("core.Verify: hash inline: %w", err)
		}

	case "Target":
		cfg := s.snapshotConfig()
		blobPath, err := blobpath.Resolve(cfg.PathTopology,
			domain.BlobTypeRegular, string(m.BlobRef))
		if err != nil {
			return fmt.Errorf("core.Verify: resolve blob path: %w", err)
		}
		rc, err := s.drv.Get(ctx, blobPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return errs.ErrCorruptedBlob
			}
			return fmt.Errorf("core.Verify: get blob: %w", err)
		}
		_, copyErr := io.Copy(hasher, rc)
		_ = rc.Close()
		if copyErr != nil {
			return fmt.Errorf("core.Verify: hash blob: %w", copyErr)
		}

	case "ExternalRef":
		return fmt.Errorf("core.Verify: BlobStorage: ExternalRef deferred to a later milestone")

	default:
		return fmt.Errorf("core.Verify: unknown BlobStorage %q", m.LayoutHeader.BlobStorage)
	}

	if !bytes.Equal(hasher.Sum(nil), want) {
		return errs.ErrCorruptedBlob
	}
	return nil
}
