package store

import (
	"context"
	"errors"
	"io"
	"os"

	"scrinium.dev/domain"
	"scrinium.dev/engine/layout"
)

// HeadlessDataPlane is the narrow seam for storing large external payloads as
// headless blob-backed data artifacts (ADR-105). It is deliberately NOT part
// of the public Store API: a headless write bypasses the handle/identity
// machinery (the artifact is reachable only by its digest, not a floating
// handle) and is engine-internal — the checkpoint agent is its first consumer,
// threading the returned digest into a pointer-envelope's external_payload_ref.
// Interface segregation, not a flag on Put: only a consumer that holds this
// seam can write headless. Resolution and deletion of these payloads are not
// here — they happen transparently through the systemstore (Get resolves, and
// Delete cascades) via the store's ExternalResolver methods below.
//
// The concrete *store satisfies it; an engine-internal caller obtains it via
// HeadlessOf (the same type-assertion seam pattern as ManifestKeyProvider).
type HeadlessDataPlane interface {
	// WriteHeadless materializes input to a physical blob, wraps it in a
	// container manifest (empty identity slot, blob_refs=[blob]) that is
	// persisted and indexed, and returns the ManifestDigest — the stable
	// external reference. The body follows the store's ManifestCrypto.
	WriteHeadless(ctx context.Context, input io.Reader) (domain.ManifestDigest, error)
}

// HeadlessOf exposes the headless data plane of a concrete store. It returns
// ok=false for a Store implementation that is not the engine's own *store
// (mirrors ManifestKeyProvider's seam discipline).
func HeadlessOf(s Store) (HeadlessDataPlane, bool) {
	c, ok := s.(*store)
	return c, ok
}

// WriteHeadless borrows the write DEK (Plain → nil) and the resolver's write
// KeyID exactly as a user Put does, then delegates to the cas primitive. The
// blob and its container manifest encrypt under the same key.
func (s *store) WriteHeadless(ctx context.Context, input io.Reader) (domain.ManifestDigest, error) {
	cfg := s.snapshotConfig()
	keyID := s.resolveWriteKeyID()
	var digest domain.ManifestDigest
	err := s.withWriteDEK(cfg, func(dek []byte) error {
		var e error
		digest, e = s.contentIO().WriteHeadless(ctx, cfg, input, dek, keyID)
		return e
	})
	if err != nil {
		return "", err
	}
	return digest, nil
}

// OpenExternal resolves an external_payload_ref through the cas by-digest path
// (no handle indirection) and returns a ReadHandle over its plaintext bytes.
// It satisfies systemstore.ExternalResolver, so systemstore.Get can serve a
// pointer artifact's payload transparently.
func (s *store) OpenExternal(ctx context.Context, ref domain.ManifestDigest) (domain.ReadHandle, error) {
	return s.contentIO().OpenHandleByDigest(ctx, ref, s.crypto.KeyProvider(), string(s.snapshotConfig().ContentHasher))
}

// DeleteExternal removes the headless data artifact named by ref: the index
// row (which decrements the blob's ref_count, letting GC reap it) and the
// manifest file. It satisfies systemstore.ExternalResolver, so deleting a
// pointer artifact cascades to its payload (ADR-105). Keyed by digest — a
// headless artifact has no handle. A missing manifest file is not an error
// (the index row is the source of truth; an orphan file is GC's to reap).
func (s *store) DeleteExternal(ctx context.Context, ref domain.ManifestDigest) error {
	if err := s.index.DeleteManifest(ctx, ref); err != nil {
		return err
	}
	manifestPath, err := layout.ManifestPath(ref)
	if err != nil {
		return err
	}
	if err := s.drv.Remove(ctx, manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
