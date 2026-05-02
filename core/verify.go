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
)

// Verify performs a full integrity check of an artifact:
//
//  1. loadManifest, which itself verifies ArtifactID = hash(file
//     bytes) and decrypts the body for MetadataOnly/Envelope via
//     the configured KeyResolver.
//  2. Re-hash the blob bytes, compare to manifest.ContentHash.
//     On divergence emits EventScrubFailed and returns
//     errs.ErrCorruptedBlob.
//
// Manifest encryption is transparent here — the integration
// happens entirely in loadManifest.
//
// Currently supports BlobManifest with Inline and Target layouts,
// no Pipeline transforms. TOC, Pack, ExternalRef return explicit
// "not yet implemented" errors. Pipeline-transformed blobs (zstd
// compression, AES-GCM plugin from M2.1) require inverse-decoder
// verification and are tracked in the backlog.
func (s *store) Verify(ctx context.Context, id domain.ArtifactID) error {
	if err := s.enterRead(ctx); err != nil {
		return err
	}
	if id == "" {
		return errs.ErrArtifactNotFound
	}

	manifest, err := s.loadManifest(ctx, id)
	if err != nil {
		return err
	}

	if err := dispatchManifestType(manifest, "core.Verify"); err != nil {
		return err
	}

	if len(manifest.Pipeline) > 0 {
		// Verifying a Pipeline-transformed blob requires
		// inverting the decoder chain (read on-disk bytes,
		// run them through the inverse stages, hash the
		// plaintext, compare to ContentHash). That path is
		// not yet wired — Verify currently only validates
		// blobs whose on-disk bytes equal their plaintext.
		return fmt.Errorf("core.Verify: Pipeline-transformed blob verification not yet implemented")
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
	case domain.LayoutInline:
		if _, err := hasher.Write(m.InlineBlob); err != nil {
			return fmt.Errorf("core.Verify: hash inline: %w", err)
		}

	case domain.LayoutTarget:
		// PhysicalAddress is sourced from the index — see the
		// layout invariant in Internals/01. The read-path follows
		// what the index recorded at IndexManifest time, not what
		// the current PathTopology would compute.
		addr, err := s.index.Resolve(string(m.BlobRef))
		if err != nil {
			return fmt.Errorf("core.Verify: resolve blob path: %w", err)
		}
		rc, err := s.drv.Get(ctx, addr.Path)
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

	case domain.LayoutExternalRef:
		return fmt.Errorf("%w: core.Verify on BlobStorage=ExternalRef awaits driver.Open URI dispatch", errs.ErrNotImplemented)

	default:
		return fmt.Errorf("core.Verify: unknown BlobStorage %q", m.LayoutHeader.BlobStorage)
	}

	if !bytes.Equal(hasher.Sum(nil), want) {
		return errs.ErrCorruptedBlob
	}
	return nil
}
