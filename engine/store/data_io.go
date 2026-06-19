package store

import (
	"context"
	"errors"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/internal/cas"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/errs"
)

// data_io.go — the shared data-plane plumbing on *store: the per-operation
// content-IO and pipeline handles, manifest load, and write-time DEK custody.
// These are the helpers the read (Get/Verify) and write (Put/PutBlob) paths
// build on; they live together here rather than scattered across the
// operation files.

// contentIO builds an cas.IO bound to this store's driver, index,
// and registries. The value is a cheap stateless handle, constructed per
// operation rather than held as a field.
func (s *store) contentIO() *cas.IO {
	return cas.New(s.drv, s.index, s.hashes, s.transformers)
}

// loadManifest reads, verifies, and decodes the manifest file for id via
// the artifact I/O layer. Used by Get, Delete, and Verify. Returns
// ErrArtifactNotFound when the file is absent and ErrCorruptedManifest
// when its hash does not match id. State checks are the caller's job.
//
// Decode dispatches on the file header: Plain bypasses the resolver;
// encrypted (Sealed/Paranoid) consults the snapshotted resolver. A Locked
// Store has a nil resolver, which surfaces ErrKeyNotFound — the correct
// refusal. asKeyProvider maps a nil resolver to a nil provider (the
// typed-nil guard).
func (s *store) loadManifest(ctx context.Context, id domain.ArtifactID) (domain.Manifest, error) {
	return s.contentIO().Load(ctx, id, s.crypto.KeyProvider(), string(s.snapshotConfig().ContentHasher))
}

// The store↔pipeline glue. The transform engine (Encoder/Decoder chain,
// three-hash teeing, inverse read chain) lives in pipeline.Runner; what
// stays here is store policy plus the accessor that binds a Runner to
// this store's registries.

// pipelineRunner returns a Runner bound to this store's hash and
// transformer registries. A Runner is a cheap wrapper, built per
// operation rather than held as a field, so it stays out of the
// &store{} construction sites. s.hashes / s.transformers remain on the
// store: VerifyOnRead consults s.transformers directly, and manifest /
// system.config hashing use s.hashes.
func (s *store) pipelineRunner() *pipeline.Runner {
	return pipeline.NewRunner(s.hashes, s.transformers)
}

// errPipelineWithInline is returned when an Inline blob would have to
// flow through a non-empty Pipeline — not yet supported. Store policy,
// so it lives here rather than in the engine.
var errPipelineWithInline = errors.New(
	"store.Put: Pipeline transforms on Inline blobs are not supported in M2.1")

// withWriteDEK borrows a DEK copy for an encrypting write and guarantees
// it is wiped before returning. For a Plain config it calls fn with a
// nil DEK. The DEK never escapes fn, so no write path can leak it by
// forgetting to wipe. The write KeyID is resolved by the caller (Put)
// and no longer threaded here — withWriteDEK is now purely DEK custody.
func (s *store) withWriteDEK(cfg domain.StoreConfig, fn func(dek []byte) error) error {
	if cfg.ManifestCrypto == "" || cfg.ManifestCrypto == domain.ManifestCryptoPlain {
		return fn(nil)
	}
	dek, err := s.crypto.DEKForWrite(cfg.ManifestCrypto)
	if err != nil {
		return err
	}
	defer aead.Wipe(dek)
	return fn(dek)
}

// resolveWriteKeyID asks the resolver which KeyID a new artifact
// encrypts under. The resolver reference is snapshotted under
// the crypto lock but ResolveWriteKey runs without it
// — it must be a cheap, non-blocking lookup. Returns "" for an
// unencrypted store.
func (s *store) resolveWriteKeyID() string {
	r := s.crypto.Resolver()
	if r == nil {
		return ""
	}
	return r.ResolveWriteKey(pipeline.KeyContext{})
}

// guardHandleless enforces the negative identity invariant (ADR-83): a
// manifest with an empty identity slot (handle IS NULL) is not a
// user-visible artifact — a pack container or other engine-internal
// object — so user-facing Get/Delete/Verify collapse it to not-found
// rather than leaking it. Structure (chunked/composite bodies) is no
// longer dispatched here: the owning wrapper handles it (ADR-88), and a
// body whose layout needs an absent decorator fails in the open path.
func guardHandleless(m domain.Manifest) error {
	if !m.IsUser() {
		return errs.ErrArtifactNotFound
	}
	return nil
}
