package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/artifactwriter"
)

// Put records an artifact and returns its ArtifactID. It is the
// orchestrator for the write path: it applies the entry gate, validates
// inputs against the active config, resolves the write key and borrows
// the DEK (the only crypto-locked steps), and delegates the physical
// mechanics — blob materialization, manifest assembly, persistence — to
// artifactwriter. The store keeps the policy and the secrets;
// artifactwriter keeps the I/O.
func (s *store) Put(ctx context.Context, a domain.Artifact, opts domain.PutOptions) (domain.ArtifactID, error) {
	if err := s.enterWrite(ctx); err != nil {
		return "", err
	}
	if err := validatePutInputs(a, opts); err != nil {
		return "", err
	}

	cfg := s.snapshotConfig()
	if err := s.checkPutSupported(cfg, opts); err != nil {
		return "", err
	}

	aw := artifactwriter.New(s.drv, s.index, s.hashes, s.transformers)

	// Resolve the write KeyID once and thread it through both the blob
	// pipeline and the manifest body, so a blob and its manifest encrypt
	// under the same key.
	writeKeyID := s.resolveWriteKeyID(opts.Namespace)

	blob, err := aw.Materialize(ctx, cfg, a, opts, writeKeyID)
	if err != nil {
		return "", fmt.Errorf("store.Put: %w", err)
	}

	// Borrow the DEK under the crypto lock only for the duration of the
	// ComputeArtifactID call inside AssembleManifest; withWriteDEK wipes
	// the copy immediately after. For a Plain config dek is nil.
	var (
		manifest      domain.Manifest
		manifestBytes []byte
	)
	if err := s.withWriteDEK(cfg, func(dek []byte) error {
		var aerr error
		manifest, manifestBytes, aerr = aw.AssembleManifest(cfg, a, opts, blob, dek, writeKeyID)
		return aerr
	}); err != nil {
		return "", fmt.Errorf("store.Put: %w", err)
	}

	if err := aw.PersistManifest(ctx, manifest, manifestBytes, blob.Addr); err != nil {
		return "", fmt.Errorf("store.Put: %w", err)
	}

	s.publish(event.EventManifestSaved, event.ManifestSavedPayload{Manifest: manifest})

	// Lock-free diagnostic trace (ADR-60): emitted after the manifest is
	// persisted, with every crypto lock released and the DEK copy already
	// wiped by withWriteDEK. Logs the opaque write KeyID, never the key.
	// LogAttrs avoids allocating an []any; on a discard logger Enabled is
	// false and the attrs are never evaluated further.
	log := s.componentLogger("store")
	if log.Enabled(ctx, slog.LevelDebug) {
		log.LogAttrs(ctx, slog.LevelDebug, "put committed",
			storeIDAttr(s),
			slog.String("artifact_id", string(manifest.ArtifactID)),
			slog.String("namespace", opts.Namespace),
			manifestCryptoAttr(cfg.ManifestCrypto),
			keyIDAttr(writeKeyID),
		)
	}
	return manifest.ArtifactID, nil
}

// withWriteDEK borrows a DEK copy for an encrypting write and guarantees
// it is wiped before returning. For a Plain config it calls fn with a
// nil DEK. The DEK never escapes fn, so no write path can leak it by
// forgetting to wipe. The write KeyID is resolved by the caller (Put)
// and no longer threaded here — withWriteDEK is now purely DEK custody.
func (s *store) withWriteDEK(cfg domain.StoreConfig, fn func(dek []byte) error) error {
	if cfg.ManifestCrypto == "" || cfg.ManifestCrypto == domain.ManifestCryptoPlain {
		return fn(nil)
	}
	dek, err := s.crypto.dekForWrite(cfg.ManifestCrypto)
	if err != nil {
		return err
	}
	defer aead.Wipe(dek)
	return fn(dek)
}

// resolveWriteKeyID asks the resolver which KeyID a new artifact in this
// namespace encrypts under. The resolver reference is snapshotted under
// the crypto lock but ResolveWriteKey runs without it
// — it must be a cheap, non-blocking lookup. Returns "" for an
// unencrypted store.
func (s *store) resolveWriteKeyID(namespace string) string {
	r := s.crypto.resolver()
	if r == nil {
		return ""
	}
	return r.ResolveWriteKey(pipeline.KeyContext{Namespace: namespace})
}

// checkPutSupported rejects configurations and options whose support is
// not yet wired, before any I/O. These are invariants of the active
// config plus the per-call BlobType; catching them here keeps the write
// path free of feature-gate branches.
func (s *store) checkPutSupported(cfg domain.StoreConfig, opts domain.PutOptions) error {
	if err := s.pipelineRunner().ValidateAlgos(cfg.Pipeline); err != nil {
		return fmt.Errorf("store.Put: %w", err)
	}
	if opts.BlobType != "" && opts.BlobType != domain.BlobTypeRegular {
		return fmt.Errorf("store.Put: BlobType %q not supported (TODO M3)", opts.BlobType)
	}
	if cfg.BlobStorage == domain.BlobStorageExternalRef {
		return errors.New("store.Put: BlobStorage: ExternalRef not yet supported")
	}
	if cfg.ManifestStorage != domain.ManifestStorageRemote && cfg.ManifestStorage != "" {
		// Local and Replicated need HostStorage as the transit buffer,
		// not yet wired (TODO M4.2); only Remote (the default) works.
		return fmt.Errorf("store.Put: ManifestStorage %q requires HostStorage (TODO M4.2)", cfg.ManifestStorage)
	}
	if cfg.BlobStorage == domain.BlobStorageInlineFallback && cfg.InlineBlobLimit > 0 && len(cfg.Pipeline) > 0 {
		// Inline + Pipeline is reserved (M2-extra); refuse early so a
		// user never gets untransformed bytes inside the manifest.
		return errPipelineWithInline
	}
	return nil
}

// validatePutInputs covers the cheap, side-effect-free checks that
// reject before any I/O.
func validatePutInputs(a domain.Artifact, opts domain.PutOptions) error {
	if a.Payload == nil && opts.ExternalURI == "" {
		return errors.New("store.Put: nil Payload and no ExternalURI")
	}
	if len(opts.Namespace) > domain.MaxNamespaceLen {
		return errs.ErrNamespaceTooLong
	}
	if strings.HasPrefix(opts.Namespace, domain.NamespaceSystemPrefix) || opts.Namespace == domain.NamespaceWildcard {
		return errs.ErrReservedNamespace
	}
	if len(opts.SessionID) > domain.MaxSessionIDLen {
		return errs.ErrSessionIDTooLong
	}
	if len(a.Ext) > domain.MaxExtSize {
		return errs.ErrExtTooLarge
	}
	if len(a.Usr) > domain.MaxUsrSize {
		return errs.ErrUsrTooLarge
	}
	return nil
}

// PutBlob is the decorator entry point (chunker.Wrapper) for writing
// anonymous chunks without a manifest. Not yet implemented: the stub
// returns ErrNotImplemented rather than silently succeeding.
func (s *store) PutBlob(ctx context.Context, r io.Reader, blobType domain.BlobType) (domain.ContentHash, error) {
	return "", fmt.Errorf("%w: store.PutBlob is deferred to M5 (chunker.Wrapper); the method moves to BlobStore at M5 start",
		errs.ErrNotImplemented)
}
