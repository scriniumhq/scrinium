package store

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// Verify performs a full integrity check of an artifact:
//
//  1. loadManifest, which itself verifies ArtifactID = hash(file bytes)
//     and decrypts the body for Sealed/Paranoid via the configured
//     KeyResolver.
//  2. Re-hash the blob plaintext bytes (casio.VerifyBlob), comparing
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
// The integrity mechanics live in casio; the store owns the policy
// (state gate, manifest-type dispatch) and the consequence (event +
// log). casio returns ErrCorruptedBlob; the store publishes
// EventScrubFailed (ADR-60: errors return, events publish, logs explain).
//
// Handleless manifests (empty identity slot) collapse to not-found via
// guardHandleless; bodies whose layout needs an absent decorator fail in
// the verify path.
func (d dataFacet) Verify(ctx context.Context, id domain.ArtifactID) error {
	if err := d.enterRead(ctx); err != nil {
		return err
	}
	if id == "" {
		return errs.ErrArtifactNotFound
	}

	manifest, err := d.loadManifest(ctx, id)
	if err != nil {
		return err
	}
	if err := guardHandleless(manifest); err != nil {
		return err
	}

	if err := d.casio().VerifyBlob(ctx, manifest); err != nil {
		d.publish(event.EventScrubFailed, event.ScrubFailedPayload{
			ArtifactID: id,
			Err:        err,
		})
		// The event notifies; the log explains. Warn — integrity
		// verification failed for a specific artifact, recoverable at the
		// operator level (restore from backup / investigate medium).
		// Lock-free: Verify holds no mutex.
		d.componentLogger("store").LogAttrs(ctx, slog.LevelWarn, "verify failed: blob integrity mismatch",
			storeIDAttr(d.core), artifactIDAttr(id))
		return err
	}
	return nil
}

// VerifyBlobRef performs the same plaintext integrity check as Verify
// but keyed by a physical blob_ref rather than an artifact id. It is
// the Scrub Agent's expensive per-blob step: ListUnverified yields
// blob_refs, and this re-hashes the blob through the inverse pipeline
// and compares against the expected ContentHash.
//
// Resolving the pipeline requires a full manifest (the index row has no
// Pipeline/LayoutHeader — those live in the manifest file), so this
// loads the file manifest of any one consuming artifact. For a blob
// shared by several artifacts (dedup) every consumer carries the same
// content-derived pipeline and ContentHash, so any one is a valid
// source — VerifyBlobRef uses the first the index yields.
//
// On a hash mismatch it publishes EventScrubFailed (tagging the
// consumer whose manifest was used) and returns errs.ErrCorruptedBlob.
// A blob_ref with no consuming manifest (a race against Delete/GC, or
// an orphan blob) returns errs.ErrArtifactNotFound, which the Scrub
// Agent treats as "skip, not a corruption".
func (d dataFacet) VerifyBlobRef(ctx context.Context, blobRef string) error {
	if err := d.enterRead(ctx); err != nil {
		return err
	}
	if blobRef == "" {
		return errs.ErrArtifactNotFound
	}

	// First consuming manifest wins. ManifestsByBlobRef yields the
	// index-resident shape; we only need its ArtifactID to load the
	// full file manifest (with Pipeline) below. fs.SkipAll stops the
	// scan after the first row (iterateManifestRows treats it as a
	// clean stop, returning nil).
	var consumerID domain.ArtifactID
	found := false
	err := d.index.ManifestsByBlobRef(ctx, blobRef, func(m domain.Manifest) error {
		consumerID = m.ArtifactID
		found = true
		return fs.SkipAll
	})
	if err != nil {
		return err
	}
	if !found {
		return errs.ErrArtifactNotFound
	}

	manifest, err := d.loadManifest(ctx, consumerID)
	if err != nil {
		return err
	}
	if err := guardHandleless(manifest); err != nil {
		return err
	}

	if err := d.casio().VerifyBlob(ctx, manifest); err != nil {
		d.publish(event.EventScrubFailed, event.ScrubFailedPayload{
			ArtifactID: consumerID,
			Err:        err,
		})
		d.componentLogger("store").LogAttrs(ctx, slog.LevelWarn, "scrub: blob integrity mismatch",
			storeIDAttr(d.core), artifactIDAttr(consumerID))
		return err
	}
	return nil
}

// VerifyManifest checks a manifest's integrity only — that the manifest
// file still hashes to its ArtifactID — without touching the blob. It
// is the cheap half of verification: loadManifest already re-hashes the
// file via VerifyArtifactID (and decrypts Sealed/Paranoid bodies), so a
// successful load IS a successful manifest verification.
//
// It is the Scrub Agent's cascade step: after the expensive
// VerifyBlobRef confirms a physical blob, every consuming artifact's
// manifest is cheaply re-verified here and stamped. It is also the only
// check Inline artifacts need — their payload lives inside the manifest,
// so manifest integrity is the whole of their integrity.
//
// On a corrupt manifest it publishes EventScrubFailed and returns the
// underlying error (errs.ErrCorruptedManifest from VerifyArtifactID, or
// a decrypt failure). A missing manifest returns errs.ErrArtifactNotFound.
func (d dataFacet) VerifyManifest(ctx context.Context, id domain.ArtifactID) error {
	if err := d.enterRead(ctx); err != nil {
		return err
	}
	if id == "" {
		return errs.ErrArtifactNotFound
	}
	if _, err := d.loadManifest(ctx, id); err != nil {
		// loadManifest returns ErrArtifactNotFound for an absent
		// manifest (a race against Delete) — propagate it untouched so
		// the agent skips rather than alarms. Any other error is an
		// integrity failure worth an event.
		if errors.Is(err, errs.ErrArtifactNotFound) {
			return err
		}
		d.publish(event.EventScrubFailed, event.ScrubFailedPayload{
			ArtifactID: id,
			Err:        err,
		})
		d.componentLogger("store").LogAttrs(ctx, slog.LevelWarn, "scrub: manifest integrity mismatch",
			storeIDAttr(d.core), artifactIDAttr(id))
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
