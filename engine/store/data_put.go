package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/cas"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// Put records an artifact and returns its ArtifactID. It is the
// orchestrator for the write path: it applies the entry gate, validates
// inputs against the active config, resolves the write key and borrows
// the DEK (the only crypto-locked steps), and delegates the physical
// mechanics — blob materialization, manifest assembly, persistence — to
// cas. The store keeps the policy and the secrets; cas keeps the I/O.
func (s *store) Put(ctx context.Context, a domain.Artifact, opts ...domain.PutOption) (domain.ArtifactID, error) {
	if err := s.enterWrite(ctx); err != nil {
		return "", err
	}
	dopts := domain.ApplyPut(opts...)

	cfg := s.snapshotConfig()

	if err := validatePutInputs(a, dopts); err != nil {
		return "", err
	}
	if err := s.checkPutSupported(cfg, dopts); err != nil {
		return "", err
	}

	aio := cas.New(s.drv, s.index, s.hashes, s.transformers)

	// Resolve the write KeyID once and thread it through both the blob
	// pipeline and the manifest body, so a blob and its manifest encrypt
	// under the same key.
	writeKeyID := s.resolveWriteKeyID()

	blob, err := aio.Materialize(ctx, cfg, a, dopts, writeKeyID)
	if err != nil {
		return "", s.traceErr(ctx, "Put", fmt.Errorf("store.Put: %w", err), slog.String("stage", "materialize"))
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
		manifest, manifestBytes, aerr = aio.AssembleManifest(cfg, a, dopts, blob, dek, writeKeyID)
		return aerr
	}); err != nil {
		return "", s.traceErr(ctx, "Put", fmt.Errorf("store.Put: %w", err), slog.String("stage", "assemble"))
	}

	if err := aio.PersistManifest(ctx, manifest, manifestBytes, blob.Addr); err != nil {
		return "", s.traceErr(ctx, "Put", fmt.Errorf("store.Put: %w", err), slog.String("stage", "persist"))
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
			manifestCryptoAttr(cfg.ManifestCrypto),
			keyIDAttr(writeKeyID),
		)
	}
	return manifest.ArtifactID, nil
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
	if cfg.BlobStorage == domain.BlobStorageInline && cfg.InlineBlobLimit > 0 && len(cfg.Pipeline) > 0 {
		// Inline + Pipeline is reserved (M2-extra); refuse early so a
		// user never gets untransformed bytes inside the manifest.
		return errPipelineWithInline
	}
	return nil
}

// validatePutInputs covers the cheap, side-effect-free checks that
// reject before any I/O.
func validatePutInputs(a domain.Artifact, opts domain.PutOptions) error {
	if a.Payload == nil {
		return errors.New("store.Put: nil Payload")
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
