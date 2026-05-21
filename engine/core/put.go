package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/event"
	"scrinium.dev/engine/internal/blobpath"
	"scrinium.dev/engine/internal/manifestcodec"
	"scrinium.dev/engine/internal/manifestcrypto"
)

// stagingPrefix is the directory where in-flight blob writes live
// until their content hash is known. After the hash is computed
// the file is renamed to its final hash-derived path.
//
// Living under system.state keeps staging blobs out of the way of
// all other engine code: the Recovery Agent in M3 will treat
// dangling staging files as orphans and prune them. We do not
// need a HostStorage transit buffer for this: every staging blob
// is rewritten or removed within a single Put call.
const stagingPrefix = domain.NamespaceSystemState + "/staging"

// Put records an artifact in the Store. Two blob-placement paths
// are supported:
//
//	Target: payload streams to a staging file, content is hashed
//	on the fly, dedup is checked, the staging file is renamed to
//	its final hash-derived path.
//
//	Inline (chosen when StoreConfig.BlobStorage is InlineFallback
//	AND len(payload) <= InlineBlobLimit): payload is buffered in
//	memory and stored inside the manifest. No blob file is
//	produced; dedup is disabled because inline bytes have no
//	separate identity in the blobs table (docs §… "Deduplication
//	is forcibly disabled" for inline blobs).
//
// Then the manifest is built, hashed (becomes ArtifactID), and
// written to its own hash-sharded path under manifests/. Finally
// the index is updated.
//
// ExternalRef, Pipeline, Encryption, HostStorage transit, and
// Pack volumes are deferred to later milestones. Reaching a code
// path that needs them returns an explicit error.
func (s *store) Put(ctx context.Context, a domain.Artifact, opts domain.PutOptions) (domain.ArtifactID, error) {
	if err := s.enterWrite(ctx); err != nil {
		return "", err
	}
	if err := validatePutInputs(a, opts); err != nil {
		return "", err
	}

	cfg := s.snapshotConfig()

	// Pipeline check: every algorithm referenced in the active
	// config must be present in the TransformerRegistry. This is
	// per-Put rather than per-Open because a registry can be
	// extended at runtime (historical-compat use-case from docs
	// §7.3) and we want errors at the call that needs the algo,
	// not at startup.
	if err := s.validatePipelineAlgos(cfg.Pipeline); err != nil {
		return "", fmt.Errorf("core.Put: %w", err)
	}

	// Reject configurations whose support is not yet wired.
	if opts.BlobType != "" && opts.BlobType != domain.BlobTypeRegular {
		return "", fmt.Errorf("core.Put: BlobType %q not supported (TODO M3)", opts.BlobType)
	}
	if cfg.BlobStorage == domain.BlobStorageExternalRef {
		return "", errors.New("core.Put: BlobStorage: ExternalRef not yet supported")
	}
	if cfg.ManifestStorage != domain.ManifestStorageRemote && cfg.ManifestStorage != "" {
		// Local and Replicated require HostStorage as the transit
		// buffer (see 2. Internals/01 Topology and 4. API
		// Reference/05 Configuration §5). Until HostStorage is
		// wired (TODO M4.2), only Remote (the default) works.
		return "", fmt.Errorf("core.Put: ManifestStorage %q requires HostStorage (TODO M4.2)",
			cfg.ManifestStorage)
	}

	// TODO(M5.x): Two-Pass dedup path. Per docs/2. Internals/02 §2.1.1,
	// when a.Payload implements io.ReadSeeker AND the driver does not
	// declare CapSlowRead, we should hash on a first pass, probe the
	// dedup index, and skip the staging write on a hit. M1.4 ships
	// with One-Pass only — correct, but writes-then-dedups even on
	// hits. Acceptable for the M1 perimeter; revisit when S3 driver
	// arrives in M5 (CapSlowRead becomes a real signal there).

	// --- Phase 1: hash payload, decide inline vs target ---
	//
	// We always need the content hash up front: it is the dedup
	// key, the BlobRef of fresh blobs, and a deterministic input
	// to the manifest. The placement decision (inline body vs.
	// separate blob file) depends on size, which is also produced
	// here.
	//
	// For InlineFallback we speculatively read up to
	// InlineBlobLimit + 1 bytes. If the read returns at most
	// InlineBlobLimit bytes, the payload fits inline; otherwise
	// we have already consumed the head and must drain the rest
	// to staging via MultiReader.

	hashAlgo := string(cfg.ContentHasher)

	// ADR-58: the engine owns write-key choice. Resolve the KeyID
	// once and thread it into the blob Pipeline (EncodeContext) and
	// the manifest-body crypto below, so a blob and its manifest
	// are encrypted under the same KeyID. The default
	// StaticKeyResolver ignores the namespace and returns "".
	writeKeyID := s.resolveWriteKeyID(opts.Namespace)

	useInlineFallback := cfg.BlobStorage == domain.BlobStorageInlineFallback &&
		cfg.InlineBlobLimit > 0
	inlineLimit := cfg.InlineBlobLimit

	// Inline + Pipeline is reserved (M2-extra in backlog). The
	// engine refuses early so users do not silently get
	// untransformed bytes inside the manifest.
	if useInlineFallback && len(cfg.Pipeline) > 0 {
		return "", errPipelineWithInline
	}

	var (
		contentHash    domain.ContentHash
		originalSize   int64
		inlineBytes    []byte // non-nil iff this Put goes inline
		blobRef        domain.BlobRef
		blobAddr       domain.PhysicalAddress
		pipelineStages []domain.PipelineStage
	)

	if useInlineFallback {
		// Inline path: no Pipeline (refused above), no dedup probe.
		// Same as M1.4 — kept verbatim modulo the helper hashes.
		head, err := io.ReadAll(io.LimitReader(a.Payload, inlineLimit+1))
		if err != nil {
			return "", fmt.Errorf("core.Put: read payload head: %w", err)
		}
		if int64(len(head)) <= inlineLimit {
			h, err := s.hashes.NewHasher(hashAlgo)
			if err != nil {
				return "", fmt.Errorf("core.Put: hasher: %w", err)
			}
			if _, err := h.Write(head); err != nil {
				return "", fmt.Errorf("core.Put: hash inline: %w", err)
			}
			contentHash = domain.ContentHash(s.hashes.Format(hashAlgo, h.Sum(nil)))
			originalSize = int64(len(head))
			inlineBytes = head
			blobRef = domain.BlobRef(contentHash)
			pipelineStages = []domain.PipelineStage{}
			// blobAddr stays zero: no driver entry for inline.
		} else {
			// Overflowed inline → fall through to Target streaming
			// using the standard runner. Splice the consumed head
			// back in front of the remaining Payload.
			combined := io.MultiReader(bytes.NewReader(head), a.Payload)
			contentHash, blobRef, originalSize, pipelineStages, blobAddr, err =
				s.streamThroughPipeline(ctx, cfg, hashAlgo, writeKeyID, combined)
			if err != nil {
				return "", err
			}
		}
	} else {
		// Plain Target path: stream straight through the Pipeline
		// (which may be empty — runner handles that).
		var err error
		contentHash, blobRef, originalSize, pipelineStages, blobAddr, err =
			s.streamThroughPipeline(ctx, cfg, hashAlgo, writeKeyID, a.Payload)
		if err != nil {
			return "", err
		}
	}

	// --- Phase 2: build manifest and compute its ArtifactID ---
	//
	// LayoutHeader.BlobStorage records HOW this particular blob
	// is laid out, regardless of the StoreConfig that was in
	// effect at write time. The read path inspects the header,
	// not the current config — that is what makes manifests
	// stable across config changes.
	//
	// Per docs §7.2: BlobRef is set on every manifest, including
	// inline ones (where it equals the ContentHash of the
	// embedded bytes).

	layout := domain.LayoutTarget
	if inlineBytes != nil {
		layout = domain.LayoutInline
	}
	createdAt := time.Now().UTC()
	manifest := domain.Manifest{
		Type:           domain.ManifestTypeBlob,
		Namespace:      opts.Namespace,
		SessionID:      opts.SessionID,
		CreatedAt:      createdAt,
		ContentHash:    contentHash,
		OriginalSize:   originalSize,
		BlobRef:        blobRef,
		LayoutHeader:   domain.LayoutHeader{BlobStorage: layout},
		Pipeline:       pipelineStages,
		InlineBlob:     inlineBytes,
		RetentionUntil: opts.RetentionUntil,
		Ext:            a.Ext,
		Usr:            a.Usr,
	}
	// Snapshot crypto state for non-Plain manifest encryption.
	// Held briefly under cryptoMu, then released so a parallel
	// Unlock/RotateKEK is not blocked by a long-running Put.
	// dek is copied; keyResolver is an immutable interface so a
	// reference is enough.
	var dekSnapshot []byte
	var keyID string
	if cfg.ManifestCrypto != "" && cfg.ManifestCrypto != domain.ManifestCryptoPlain {
		s.cryptoMu.Lock()
		if len(s.dek) == 0 {
			s.cryptoMu.Unlock()
			return "", fmt.Errorf("%w: ManifestCrypto=%q requires Unlock",
				errs.ErrLocked, cfg.ManifestCrypto)
		}
		if s.keyResolver == nil {
			s.cryptoMu.Unlock()
			return "", fmt.Errorf("core.Put: ManifestCrypto=%q requires WithKeyResolver or default-resolver promotion",
				cfg.ManifestCrypto)
		}
		dekSnapshot = append([]byte{}, s.dek...)
		keyID = writeKeyID
		s.cryptoMu.Unlock()
		defer manifestcrypto.Wipe(dekSnapshot)
	}

	artifactID, manifestBytes, signedManifest, err := manifestcodec.ComputeArtifactID(
		manifest, hashAlgo, s.hashes,
		cfg.ManifestEncoding, cfg.ManifestCrypto,
		dekSnapshot, keyID,
	)
	if err != nil {
		// On encoding/crypto deferral the blob (if any) is already
		// committed. We do NOT roll it back: the orphan blob is
		// harmless (ref_count stays 0, GC reaps it). Rolling back
		// would require an inverse of Driver.Rename, which can
		// race against a parallel Put deduping on the same content.
		return "", fmt.Errorf("core.Put: compute artifact id: %w", err)
	}
	manifest = signedManifest

	// --- Phase 3: write the manifest file ---

	manifestPath, err := blobpath.ManifestPath(artifactID)
	if err != nil {
		return "", fmt.Errorf("core.Put: manifest path: %w", err)
	}
	if err := s.drv.Put(ctx, manifestPath, bytes.NewReader(manifestBytes)); err != nil {
		return "", fmt.Errorf("core.Put: write manifest: %w", err)
	}

	// --- Phase 4: index ---
	//
	// For inline manifests blobAddr is the zero PhysicalAddress.
	// IndexManifest dispatches on manifest.LayoutHeader.BlobStorage
	// to skip the blobs-table insertion for inline; the manifest
	// itself is still indexed so Walk and GetBySession find it.

	if err := s.index.IndexManifest(ctx, manifest, blobAddr, nil, nil); err != nil {
		// Manifest file is on disk but unindexed. RebuildIndexAgent
		// (M3) is the recovery path. We surface the error so the
		// caller can retry the index step or reissue Put (which
		// will dedup the blob and re-attempt the manifest).
		return "", fmt.Errorf("core.Put: index manifest: %w", err)
	}

	// --- Phase 5: emit ---

	s.publish(EventManifestSaved, ManifestSavedPayload{
		Manifest:  manifest,
		IsTransit: false,
	})

	return artifactID, nil
}

// streamThroughPipeline runs the active Pipeline over input and
// commits the resulting bytes to a blob slot. It is the shared
// tail of every Put path that ends up on disk.
//
// The hashAlgo is taken from cfg.ContentHasher; we accept it as
// an argument so the caller does not need to read it twice.
func (s *store) streamThroughPipeline(
	ctx context.Context,
	cfg domain.StoreConfig,
	hashAlgo string,
	writeKeyID string,
	input io.Reader,
) (
	contentHash domain.ContentHash,
	blobRef domain.BlobRef,
	originalSize int64,
	pipelineStages []domain.PipelineStage,
	blobAddr domain.PhysicalAddress,
	err error,
) {
	stagingPath := s.makeStagingPath()

	streamReader, pp, err := s.buildPutPipeline(hashAlgo, input, cfg.Pipeline, EncodeContext{KeyID: writeKeyID})
	if err != nil {
		return "", "", 0, nil, domain.PhysicalAddress{}, fmt.Errorf("core.Put: %w", err)
	}

	// originalSize must measure the ORIGINAL payload, not the
	// post-Pipeline output. We count via a tee on the input layer
	// — the runner already tees content for the hasher, but counter
	// and hasher are separate concerns.
	counter := &countingReader{r: streamReader}

	if err := s.drv.Put(ctx, stagingPath, counter); err != nil {
		return "", "", 0, nil, domain.PhysicalAddress{},
			fmt.Errorf("core.Put: stage payload: %w", err)
	}

	contentHash, blobRef, pipelineStages = pp.finalize(s.hashes.Format)

	// counter.n now equals the byte count of the FINAL stream
	// (post-Pipeline). originalSize must come from the
	// pre-Pipeline tee — see comment below.
	originalSize = pp.contentBytesRead()

	commitRef, addr, err := s.commitBlob(ctx, cfg, stagingPath, contentHash,
		originalSize, blobRef)
	if err != nil {
		return "", "", 0, nil, domain.PhysicalAddress{}, err
	}
	return contentHash, commitRef, originalSize, pipelineStages, addr, nil
}

// commitBlob is the tail of the Target write path: dedup probe,
// then either drop the staging file (hit) or rename it to its
// final hash-derived path (miss). Returns the BlobRef and the
// PhysicalAddress of where the blob actually lives now.
func (s *store) commitBlob(
	ctx context.Context,
	cfg domain.StoreConfig,
	stagingPath string,
	contentHash domain.ContentHash,
	originalSize int64,
	blobRef domain.BlobRef,
) (domain.BlobRef, domain.PhysicalAddress, error) {
	existingRef, found, err := s.index.ExistsByContent(ctx, contentHash, originalSize)
	if err != nil {
		_ = s.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("core.Put: dedup probe: %w", err)
	}
	if found {
		if err := s.drv.Remove(ctx, stagingPath); err != nil {
			return "", domain.PhysicalAddress{}, fmt.Errorf("core.Put: drop staging: %w", err)
		}
		addr, err := s.index.Resolve(ctx, existingRef)
		if err != nil {
			return "", domain.PhysicalAddress{}, fmt.Errorf("core.Put: resolve existing blob: %w", err)
		}
		return domain.BlobRef(existingRef), addr, nil
	}
	finalPath, err := blobpath.Resolve(cfg.PathTopology, domain.BlobTypeRegular, string(blobRef))
	if err != nil {
		_ = s.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("core.Put: resolve blob path: %w", err)
	}
	if err := s.drv.Rename(ctx, stagingPath, finalPath); err != nil {
		_ = s.drv.Remove(ctx, stagingPath)
		return "", domain.PhysicalAddress{}, fmt.Errorf("core.Put: commit blob: %w", err)
	}
	return blobRef, domain.PhysicalAddress{
		Workspace: domain.WorkspaceLocation,
		Path:      finalPath,
	}, nil
}

// snapshotConfig returns a copy of the current active StoreConfig.
// Cheap because StoreConfig is value-typed; the lock is held only
// for the duration of the copy.
func (s *store) snapshotConfig() domain.StoreConfig {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.activeConfig
}

// resolveWriteKeyID asks the KeyResolver which KeyID a new artifact
// in this namespace should be encrypted under (ADR-58). The
// resolver reference is snapshotted under cryptoMu but ResolveWriteKey
// is called without the lock held — it must be cheap and must not
// block (a map lookup), so a long-running custom resolver cannot
// stall a parallel Unlock/RotateKEK. Returns "" when no resolver is
// configured (unencrypted store).
func (s *store) resolveWriteKeyID(namespace string) string {
	s.cryptoMu.Lock()
	r := s.keyResolver
	s.cryptoMu.Unlock()
	if r == nil {
		return ""
	}
	return r.ResolveWriteKey(KeyContext{Namespace: namespace})
}

// checkWritable extends checkOperational with the ReadOnly check.
// Used at the entry of every mutating method; read-only operations
// (Walk, Capacity, Get) use checkOperational alone.
func (s *store) checkWritable() error {
	if err := s.checkOperational(); err != nil {
		return err
	}
	if s.maintenanceMode() == domain.MaintenanceModeReadOnly {
		return errs.ErrStoreReadOnly
	}
	return nil
}

// Entry-preamble contract:
//
// Every public Store method MUST start with one of three
// canonical preambles:
//
//   - enterRead  — read-path methods (Get, Walk, Verify, Capacity,
//                  ExportRecoveryKit). Reject if state is Locked.
//   - enterWrite — write-path methods (Put, Delete, RollbackSession,
//                  UpdateConfig, SetPassphrase, RotateKEK). Same as
//                  enterRead plus the ReadOnly maintenance check.
//   - enterAdmin — admin methods that legitimately run in Locked
//                  (Unlock — its purpose is to leave Locked).
//                  Same as enterRead minus the Locked check.
//
// All three uniformly handle: ctx cancellation, closed-store
// refusal (os.ErrClosed), corrupted refusal, offline refusal,
// bootstrapping refusal. They differ only in how they treat
// Locked and ReadOnly.
//
// The set of methods that do NOT start with one of these is
// intentionally limited to: State, Capabilities, Config (pure
// in-memory readers), SetMaintenanceMode (the very escape hatch
// that toggles the regime), and Close (the gate itself).
// Any new method outside that set should start with enterRead/
// enterWrite/enterAdmin — no exceptions, no clever locality.

// enterRead is the canonical entry-preamble for read-path methods
// (Get, Verify, Walk, WalkSystem, Capacity, ConfigHistory,
// ExportRecoveryKit). Combines context cancellation with the
// priority-of-checks gate. Unlock uses enterAdmin instead, since
// Locked is its working state.
func (s *store) enterRead(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.checkOperational()
}

// enterWrite is the write-path counterpart: ctx + checkWritable
// (which itself adds the ReadOnly guard on top of checkOperational).
// Used by Put, Delete, RollbackSession, UpdateConfig, and the
// descriptor-mutating admin methods (SetPassphrase, RotateKEK).
// Those admin methods follow up with their own crypto-state checks
// after taking cryptoMu — enterWrite handles only the universal
// gate; specifics stay with each method.
func (s *store) enterWrite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.checkWritable()
}

// enterAdmin is the entry-preamble for admin methods that may
// legitimately run in StateLocked — Unlock is the canonical
// example, since its purpose is to leave Locked. Behaves like
// enterRead but treats Locked as acceptable; every other gate
// (closed / corrupted / offline / bootstrapping) still applies.
//
// Used only by Unlock today. ExportRecoveryKit, SetPassphrase,
// RotateKEK reject Locked themselves and so go through enterRead
// or enterWrite, which treat Locked as a refusal.
func (s *store) enterAdmin(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.stateMu.RLock()
	closed := s.closed
	state := s.state
	mode := s.maintenance
	s.stateMu.RUnlock()

	if closed {
		return os.ErrClosed
	}
	if state == domain.StateCorrupted {
		return errs.ErrStoreCorrupted
	}
	if mode == domain.MaintenanceModeOffline {
		return errs.ErrStoreOffline
	}
	if state == domain.StateBootstrapping {
		return errs.ErrStoreNotReady
	}
	// Locked is intentionally NOT checked here — admin callers
	// (Unlock) are the means of leaving Locked.
	return nil
}

// validatePutInputs covers the cheap, side-effect-free checks that
// must reject before any I/O. Order matches the priority of
// docs/2. Internals/01 §1.4.
func validatePutInputs(a domain.Artifact, opts domain.PutOptions) error {
	if a.Payload == nil && opts.ExternalURI == "" {

		return errors.New("core.Put: nil Payload and no ExternalURI")
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

// publish emits an event when a Publisher is configured. Cheap when
// nil — the common case for tests and minimal-stack hosts.
func (s *store) publish(typ string, payload any) {
	if s.pub == nil {
		return
	}
	s.pub.Publish(event.Event{Type: typ, Payload: payload})
}

// makeStagingPath returns a fresh, unique path under
// system.state/staging/. Uniqueness is provided by the UUID v4
// helper. A future improvement (multi-host) is to mix in a
// host_id (TODO M3.1).
func (s *store) makeStagingPath() string {
	return stagingPrefix + "/" + uuid.NewString()
}

// countingReader wraps an io.Reader and tracks the number of bytes
// passed through. We need OriginalSize and the hash in one stream;
// a TeeReader gives us the hash, but the byte count comes from
// here.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
