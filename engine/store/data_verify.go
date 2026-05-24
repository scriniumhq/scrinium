package store

import (
	"context"
	"log/slog"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/pipeline"
)

// Verify performs a full integrity check of an artifact:
//
//  1. loadManifest, which itself verifies ArtifactID = hash(file bytes)
//     and decrypts the body for Sealed/Paranoid via the configured
//     KeyResolver.
//  2. Re-hash the blob plaintext bytes (artifactio.VerifyBlob), comparing
//     to manifest.ContentHash. On divergence the store emits
//     EventScrubFailed and returns errs.ErrCorruptedBlob.
//
// Both regular blobs (on-disk bytes == plaintext) and Pipeline-transformed
// blobs (zstd, AES-GCM, ...) are supported: the inverse Decoder chain runs
// the on-disk bytes through every stage before the hash is computed. An
// AEAD tag mismatch inside the chain surfaces as errs.ErrCorruptedBlob as
// well — for an admin operation, "decryption tag did not match" and
// "plaintext hash did not match" are the same fault category.
//
// The integrity mechanics live in artifactio; the store owns the policy
// (state gate, manifest-type dispatch) and the consequence (event +
// log). artifactio returns ErrCorruptedBlob; the store publishes
// EventScrubFailed (ADR-60: errors return, events publish, logs explain).
//
// Currently supports BlobManifest with Inline and Target layouts. TOC,
// Pack, ExternalRef return explicit "not yet implemented" errors via the
// dispatchManifestType / layout switch paths.
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

	if err := s.artifactIO().VerifyBlob(ctx, manifest); err != nil {
		s.publish(event.EventScrubFailed, event.ScrubFailedPayload{
			ArtifactID: id,
			Err:        err,
		})
		// The event notifies; the log explains. Warn — integrity
		// verification failed for a specific artifact, recoverable at the
		// operator level (restore from backup / investigate medium).
		// Lock-free: Verify holds no mutex.
		s.componentLogger("store").LogAttrs(ctx, slog.LevelWarn, "verify failed: blob integrity mismatch",
			storeIDAttr(s), artifactIDAttr(id))
		return err
	}
	return nil
}

// shouldVerifyOnRead resolves the per-Get verification decision from the
// active policy, the artifact's pipeline composition, and the driver's
// capabilities.
//
// ForceEnabled and Disabled are explicit overrides. Auto consults the
// artifact and the medium: when the pipeline includes an AEAD plugin (its
// authentication tag already detects tampering on every read) or the
// driver reports CapNativeChecksum (the medium catches silent bit rot),
// the explicit ContentHash recomputation is redundant and skipped.
//
// Empty pipeline + driver without CapNativeChecksum + Auto = on: a plain
// on-disk blob on commodity media needs the explicit check.
//
// Unknown algorithms in the pipeline are treated as non-AEAD (the registry
// returns an error and the loop continues); the conservative default keeps
// verification on for stages whose integrity guarantees the engine cannot
// prove statically.
//
// transformers may be nil. In that case Auto falls through to true
// regardless of pipeline contents — AEAD detection requires the registry.
// The Get-path always passes s.transformers; the nil branch exists for
// isolated unit tests and defensive wiring.
//
// The empty-string policy is treated as Auto. The engine's
// config_default.go promotes the zero value to Auto before the active
// config is read, so this branch only fires for callers that bypass config
// (none today; the defensive handling is cheap).
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
	for _, st := range stages {
		f, err := transformers.Get(st.Algorithm)
		if err != nil {
			continue
		}
		if _, ok := f.(pipeline.AEADCapable); ok {
			return false
		}
	}
	return true
}
