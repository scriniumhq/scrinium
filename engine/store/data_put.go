package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"scrinium.dev/domain"
	"scrinium.dev/engine/internal/aead"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/artifactio"
	"scrinium.dev/errs"
	"scrinium.dev/event"
)

// Put records an artifact and returns its ArtifactID. It is the
// orchestrator for the write path: it applies the entry gate, validates
// inputs against the active config, resolves the write key and borrows
// the DEK (the only crypto-locked steps), and delegates the physical
// mechanics — blob materialization, manifest assembly, persistence — to
// artifactio. The store keeps the policy and the secrets; artifactio
// keeps the I/O.
func (d dataFacet) Put(ctx context.Context, a domain.Artifact, opts ...PutOption) (domain.ArtifactID, error) {
	if err := d.enterWrite(ctx); err != nil {
		return "", err
	}
	dopts := applyPut(opts).toDomain()

	cfg := d.snapshotConfig()
	// Fall back to the store's default namespace when the caller left it
	// empty. Resolved before validation so the effective namespace (the
	// default included) is what gets checked and recorded.
	if dopts.Namespace == "" {
		dopts.Namespace = cfg.DefaultPutNamespace
	}

	if err := validatePutInputs(a, dopts); err != nil {
		return "", err
	}
	if err := d.checkPutSupported(cfg, dopts); err != nil {
		return "", err
	}

	aio := artifactio.New(d.drv, d.index, d.hashes, d.transformers)

	// Resolve the write KeyID once and thread it through both the blob
	// pipeline and the manifest body, so a blob and its manifest encrypt
	// under the same key.
	writeKeyID := d.resolveWriteKeyID(dopts.Namespace)

	blob, err := aio.Materialize(ctx, cfg, a, dopts, writeKeyID)
	if err != nil {
		return "", d.traceErr(ctx, "Put", fmt.Errorf("store.Put: %w", err), slog.String("namespace", dopts.Namespace), slog.String("stage", "materialize"))
	}

	// Borrow the DEK under the crypto lock only for the duration of the
	// ComputeArtifactID call inside AssembleManifest; withWriteDEK wipes
	// the copy immediately after. For a Plain config dek is nil.
	var (
		manifest      domain.Manifest
		manifestBytes []byte
	)
	if err := d.withWriteDEK(cfg, func(dek []byte) error {
		var aerr error
		manifest, manifestBytes, aerr = aio.AssembleManifest(cfg, a, dopts, blob, dek, writeKeyID)
		return aerr
	}); err != nil {
		return "", d.traceErr(ctx, "Put", fmt.Errorf("store.Put: %w", err), slog.String("namespace", dopts.Namespace), slog.String("stage", "assemble"))
	}

	if err := aio.PersistManifest(ctx, manifest, manifestBytes, blob.Addr); err != nil {
		return "", d.traceErr(ctx, "Put", fmt.Errorf("store.Put: %w", err), slog.String("namespace", dopts.Namespace), slog.String("stage", "persist"))
	}

	d.publish(event.EventManifestSaved, event.ManifestSavedPayload{Manifest: manifest})

	// Lock-free diagnostic trace (ADR-60): emitted after the manifest is
	// persisted, with every crypto lock released and the DEK copy already
	// wiped by withWriteDEK. Logs the opaque write KeyID, never the key.
	// LogAttrs avoids allocating an []any; on a discard logger Enabled is
	// false and the attrs are never evaluated further.
	log := d.componentLogger("store")
	if log.Enabled(ctx, slog.LevelDebug) {
		log.LogAttrs(ctx, slog.LevelDebug, "put committed",
			storeIDAttr(d.core),
			slog.String("artifact_id", string(manifest.ArtifactID)),
			slog.String("namespace", dopts.Namespace),
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
func (c *core) withWriteDEK(cfg domain.StoreConfig, fn func(dek []byte) error) error {
	if cfg.ManifestCrypto == "" || cfg.ManifestCrypto == domain.ManifestCryptoPlain {
		return fn(nil)
	}
	dek, err := c.crypto.dekForWrite(cfg.ManifestCrypto)
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
func (c *core) resolveWriteKeyID(namespace string) string {
	r := c.crypto.resolver()
	if r == nil {
		return ""
	}
	return r.ResolveWriteKey(pipeline.KeyContext{Namespace: namespace})
}

// checkPutSupported rejects configurations and options whose support is
// not yet wired, before any I/O. These are invariants of the active
// config plus the per-call BlobType; catching them here keeps the write
// path free of feature-gate branches.
func (c *core) checkPutSupported(cfg domain.StoreConfig, opts domain.PutOptions) error {
	if err := c.pipelineRunner().ValidateAlgos(cfg.Pipeline); err != nil {
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
func (d dataFacet) PutBlob(ctx context.Context, r io.Reader, blobType domain.BlobType) (domain.ContentHash, error) {
	return "", fmt.Errorf("%w: store.PutBlob is deferred to M5 (chunker.Wrapper); the method moves to BlobStore at M5 start",
		errs.ErrNotImplemented)
}
