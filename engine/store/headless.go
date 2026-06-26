package store

import (
	"context"
	"io"

	"scrinium.dev/domain"
)

// HeadlessDataPlane is the narrow seam for storing and resolving large
// external payloads as headless blob-backed data artifacts (ADR-105). It is
// deliberately NOT part of the public Store API: a headless write bypasses the
// handle/identity machinery (the artifact is reachable only by its digest, not
// a floating handle) and is engine-internal — the checkpoint agent is its
// first consumer, threading the returned digest into a pointer-envelope's
// external_payload_ref. Interface segregation, not a flag on Put: only a
// consumer that holds this seam can write headless.
//
// The concrete *store satisfies it; an engine-internal caller obtains it via
// HeadlessOf (the same type-assertion seam pattern as ManifestKeyProvider).
type HeadlessDataPlane interface {
	// WriteHeadless materializes input to a physical blob, wraps it in a
	// container manifest (empty identity slot, blob_refs=[blob]) that is
	// persisted and indexed, and returns the ManifestDigest — the stable
	// external reference. The body follows the store's ManifestCrypto.
	WriteHeadless(ctx context.Context, input io.Reader) (domain.ManifestDigest, error)
	// OpenHeadless resolves a digest produced by WriteHeadless and opens the
	// artifact's plaintext bytes for streaming.
	OpenHeadless(ctx context.Context, digest domain.ManifestDigest) (io.ReadCloser, error)
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

// OpenHeadless resolves the external reference through the cas by-digest path
// (no handle indirection) and decodes under the store's key provider.
func (s *store) OpenHeadless(ctx context.Context, digest domain.ManifestDigest) (io.ReadCloser, error) {
	return s.contentIO().OpenByDigest(ctx, digest, s.crypto.KeyProvider(), string(s.snapshotConfig().ContentHasher))
}
