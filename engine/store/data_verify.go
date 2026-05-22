package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/pipeline"
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
// LayoutExternalRef is rejected here — Verify cannot yet fetch external
// URIs.
func (s *store) openBlobBytes(ctx context.Context, m domain.Manifest) (io.ReadCloser, error) {
	switch m.LayoutHeader.BlobStorage {
	case domain.LayoutInline:
		return io.NopCloser(bytes.NewReader(m.InlineBlob)), nil

	case domain.LayoutTarget:
		// The physical address is sourced from the index: the read path
		// follows what the index recorded at IndexManifest time, not what
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

// shouldVerifyOnRead resolves the per-Get verification decision
// from the active policy, the artifact's pipeline composition,
// and the driver's capabilities.
//
// ForceEnabled and Disabled are explicit overrides. Auto consults
// the artifact and the medium: when the pipeline includes an AEAD
// plugin (its authentication tag already detects tampering on
// every read) or the driver reports CapNativeChecksum (the medium
// catches silent bit rot), the explicit ContentHash recomputation
// is redundant and skipped.
//
// Empty pipeline + driver without CapNativeChecksum + Auto = on:
// a plain on-disk blob on commodity media needs the explicit
// check.
//
// Unknown algorithms in the pipeline are treated as non-AEAD
// (the registry returns an error and the loop continues); the
// conservative default keeps verification on for stages whose
// integrity guarantees the engine cannot prove statically.
//
// transformers may be nil. In that case Auto falls through to
// true regardless of pipeline contents — AEAD detection requires
// the registry. The Get-path always passes s.transformers; the
// nil branch exists for isolated unit tests and defensive wiring.
//
// The empty-string policy is treated as Auto. The engine's
// config_default.go promotes the zero value to Auto before the
// active config is read, so this branch only fires for callers
// that bypass config (none today; the defensive handling is
// cheap).
func shouldVerifyOnRead(
	policy domain.VerifyOnReadPolicy,
	stages []domain.PipelineStage,
	caps driver.CapabilityMask,
	transformers pipeline.TransformerRegistry,
) bool {
	switch policy {
	case domain.VerifyOnReadForceEnabled:
		return true
	case domain.VerifyOnReadDisabled:
		return false
	}
	// Auto (or unset — treated as Auto).
	if caps.Has(driver.CapNativeChecksum) {
		return false
	}
	if transformers == nil {
		return true
	}
	for _, s := range stages {
		f, err := transformers.Get(s.Algorithm)
		if err != nil {
			continue
		}
		if _, ok := f.(pipeline.AEADCapable); ok {
			return false
		}
	}
	return true
}
