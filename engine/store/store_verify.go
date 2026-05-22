package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
)

// Verify performs a full integrity check of an artifact:
//
//  1. loadManifest, which itself verifies ArtifactID = hash(file
//     bytes) and decrypts the body for Sealed/Paranoid via
//     the configured KeyResolver.
//  2. Re-hash the blob plaintext bytes, comparing to
//     manifest.ContentHash. On divergence emits EventScrubFailed
//     and returns errs.ErrCorruptedBlob.
//
// Both regular blobs (on-disk bytes == plaintext) and Pipeline-
// transformed blobs (zstd, AES-GCM, ...) are supported: the
// inverse Decoder chain runs the on-disk bytes through every
// stage before the hash is computed. An AEAD tag mismatch inside
// the chain surfaces as errs.ErrCorruptedBlob as well — for an
// admin operation, "decryption tag did not match" and "plaintext
// hash did not match" are the same fault category (the blob is
// not whole).
//
// Manifest encryption is transparent here — the integration
// happens entirely in loadManifest.
//
// Currently supports BlobManifest with Inline and Target layouts.
// TOC, Pack, ExternalRef return explicit "not yet implemented"
// errors via the dispatchManifestType / layout switch paths.
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

	if err := dispatchManifestType(manifest, "store.Verify"); err != nil {
		return err
	}

	if err := s.verifyBlobHash(ctx, manifest); err != nil {
		s.publish(event.EventScrubFailed, event.ScrubFailedPayload{
			ArtifactID: id,
			Err:        err,
		})
		return err
	}
	return nil
}

// verifyBlobHash re-hashes blob plaintext bytes and compares
// against manifest.ContentHash. The algorithm is taken from the
// ContentHash prefix (not the current StoreConfig) so historical
// artifacts written under a previous hasher still verify.
//
// The function unifies the pipeline-less and pipeline-bearing
// paths through buildGetReader: when manifest.Pipeline is empty
// the helper returns the underlying reader as-is, so the
// non-pipeline case pays no extra cost. For pipeline-bearing
// blobs the inverse Decoder chain is applied; any decode-side
// failure (AEAD tag mismatch, decompressor error) is folded into
// errs.ErrCorruptedBlob — see the Verify doc comment for the
// rationale.
func (s *store) verifyBlobHash(ctx context.Context, m domain.Manifest) error {
	_, want, hasher, err := s.parseContentHash(m.ContentHash)
	if err != nil {
		return fmt.Errorf("store.Verify: %w", err)
	}

	// Step 1 — obtain a reader over the on-disk (ciphertext-
	// shaped) bytes. Inline reads from the manifest; Target
	// reads through the Driver.
	ciphertext, err := s.openBlobBytes(ctx, m)
	if err != nil {
		return err
	}

	// Step 2 — invert the Pipeline. Empty Pipeline returns the
	// underlying reader unchanged. Closing the plaintext reader
	// closes the underlying ciphertext reader.
	plaintext, err := s.pipelineRunner().BuildGet(m.Pipeline, ciphertext)
	if err != nil {
		// buildGetReader closed `ciphertext` on its failure path.
		return fmt.Errorf("store.Verify: build pipeline: %w", err)
	}

	// Step 3 — stream-hash. Pipeline errors (AEAD tag mismatch,
	// decompressor failures) surface here; map them to
	// ErrCorruptedBlob so the admin-side category is uniform.
	_, copyErr := io.Copy(hasher, plaintext)
	closeErr := plaintext.Close()
	if copyErr != nil {
		// Distinguish corruption from infrastructure failures:
		// a decoder-side error (decryption, decompression) is
		// corruption; a raw I/O error (driver hiccup) is not.
		// We can't introspect the inner chain reliably; the
		// conservative split treats anything but context errors
		// as corruption.
		if errors.Is(copyErr, context.Canceled) ||
			errors.Is(copyErr, context.DeadlineExceeded) {
			return copyErr
		}
		return errors.Join(errs.ErrCorruptedBlob, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("store.Verify: close blob reader: %w", closeErr)
	}

	if !bytes.Equal(hasher.Sum(nil), want) {
		return errs.ErrCorruptedBlob
	}
	return nil
}

// openBlobBytes returns an io.ReadCloser over the on-disk (or
// in-manifest) bytes of an artifact's blob, without any pipeline
// decoding applied. Closing the returned reader releases any
// underlying driver-side resources; for the Inline case Close is
// a no-op.
//
// LayoutExternalRef is rejected here — Verify does not yet know
// how to fetch external URIs.
func (s *store) openBlobBytes(ctx context.Context, m domain.Manifest) (io.ReadCloser, error) {
	switch m.LayoutHeader.BlobStorage {
	case domain.LayoutInline:
		return io.NopCloser(bytes.NewReader(m.InlineBlob)), nil

	case domain.LayoutTarget:
		// PhysicalAddress is sourced from the index — see the
		// layout invariant in Internals/01. The read-path follows
		// what the index recorded at IndexManifest time, not what
		// the current PathTopology would compute.
		addr, err := s.index.Resolve(ctx, string(m.BlobRef))
		if err != nil {
			return nil, fmt.Errorf("store.Verify: resolve blob path: %w", err)
		}
		rc, err := s.drv.Get(ctx, addr.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, errs.ErrCorruptedBlob
			}
			return nil, fmt.Errorf("store.Verify: get blob: %w", err)
		}
		return rc, nil

	case domain.LayoutExternalRef:
		return nil, fmt.Errorf("%w: store.Verify on BlobStorage=ExternalRef awaits driver.Open URI dispatch", errs.ErrNotImplemented)

	default:
		return nil, fmt.Errorf("store.Verify: unknown BlobStorage %q", m.LayoutHeader.BlobStorage)
	}
}
