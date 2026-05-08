package core

// lifecycle.go — descriptor-related helpers shared by InitStore
// (init.go) and OpenStore (open.go). Splitting the constructors
// into their own files keeps each one navigable; the common
// machinery — building a *store, healing replicas, refreshing the
// descriptor cache, bootstrap-time Unlock — lives here so neither
// constructor reaches across into the other.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/core/internal/recoverykit"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver"
	"github.com/rkurbatov/scrinium/errs"
)

// buildRecoveryKit assembles the kit text for a freshly-encrypted
// descriptor. Called at InitStore (and later RotateKEK /
// SetPassphrase) before disk I/O — a kit-build failure aborts
// the Store creation rather than producing a Store the host
// cannot recover.
func buildRecoveryKit(desc *descriptor.Descriptor, wrappedDEK []byte) ([]byte, error) {
	if desc.KDFParams == nil {
		return nil, errors.New("buildRecoveryKit: descriptor missing KDFParams")
	}
	return recoverykit.Encode(recoverykit.Kit{
		StoreID:      desc.StoreID,
		CreatedAt:    time.Now().UTC(),
		Algorithm:    desc.KDFParams.Algorithm,
		Salt:         desc.KDFParams.Salt,
		Time:         desc.KDFParams.Time,
		Memory:       desc.KDFParams.Memory,
		Threads:      desc.KDFParams.Threads,
		EncryptedDEK: wrappedDEK,
	})
}

// initEncryptedDEK is the encrypted-DEK leg of InitStore: prompt
// for a passphrase, derive the KEK via Argon2id, wrap the DEK,
// and produce the Recovery Kit. Both side-effects observable to
// the caller (descriptor mutation and kit emission) are returned
// by value — the function does not touch *desc directly so the
// Plain path stays a trivial else-branch.
//
// On any failure the caller is responsible for zeroing dek; this
// function does not own its lifetime. The passphrase buffer IS
// owned here and is wiped before return.
//
// Centralising this leg lets future variants (KMS-resolved DEK,
// hardware token) drop in beside it as siblings rather than as a
// growing if/else inside InitStore.
func initEncryptedDEK(
	ctx context.Context,
	storeID string,
	dek []byte,
	provider PassphraseProvider,
	cfgKDFParams *domain.KDFParams,
) (wrappedDEK []byte, kdfParams descriptor.KDFParams, kit []byte, err error) {
	passphrase, perr := callProvider(ctx, provider, PassphraseHint{
		StoreID: storeID,
		Reason:  "init",
	})
	if perr != nil {
		return nil, descriptor.KDFParams{}, nil, perr
	}

	// cfgKDFParams is the client-side cost override; nil means
	// "use kdf.Default()". wrapDEK handles the zero value, so we
	// dereference only when present.
	var cost domain.KDFParams
	if cfgKDFParams != nil {
		cost = *cfgKDFParams
	}
	wrapped, params, werr := wrapDEK(dek, passphrase, cost)
	zeroBytes(passphrase)
	if werr != nil {
		return nil, descriptor.KDFParams{}, nil, fmt.Errorf("wrap DEK: %w", werr)
	}

	// Build the Recovery Kit against a temporary descriptor view
	// before any disk I/O so a kit-generation failure aborts the
	// Store creation.
	probe := &descriptor.Descriptor{
		StoreID:       storeID,
		SchemaVersion: descriptor.CurrentSchemaVersion,
		KDFParams:     &params,
	}
	kitBytes, kerr := buildRecoveryKit(probe, wrapped)
	if kerr != nil {
		return nil, descriptor.KDFParams{}, nil, fmt.Errorf("build recovery kit: %w", kerr)
	}
	return wrapped, params, kitBytes, nil
}

// healReplicas applies Reconcile's repair action: writes the
// damaged or missing replica from the canonical descriptor.
// HealNone is a no-op; the four healing actions reduce to two
// distinct disk operations (write L0 only, write L1 only) since
// the canonical content already lives on the surviving side.
func healReplicas(ctx context.Context, drv driver.Driver, canonical *descriptor.Descriptor, action descriptor.ReconcileAction) error {
	switch action {
	case descriptor.HealNone:
		return nil
	case descriptor.HealL0FromL1, descriptor.HealBothFromL1:
		// HealL0FromL1: L0 was missing/corrupted, rewrite it.
		// HealBothFromL1: sequence-divergence, L1 won, rewrite L0.
		// Same disk operation; distinct names preserve diagnostic
		// detail in logs.
		return descriptor.WriteReplica(ctx, drv, canonical, descriptor.L0)
	case descriptor.HealL1FromL0, descriptor.HealBothFromL0:
		return descriptor.WriteReplica(ctx, drv, canonical, descriptor.L1)
	default:
		return fmt.Errorf("core: unknown ReconcileAction %d", int(action))
	}
}

// refreshDescriptorCache compares the L2 cache against canonical
// and rewrites it when out of sync.
//
// Three branches that all reduce to "save":
//   - cache absent (loadDescriptorCache returned nil, nil)
//   - cache load failed (corruption, partial state)
//   - cache present but checksum diverges from canonical
//
// The "load failed" branch swallows the load error on purpose:
// the cache is a fast-start aid, not authoritative, and a
// damaged cache is fully recoverable from Location.
func refreshDescriptorCache(ctx context.Context, idx metaStore, canonical *descriptor.Descriptor) error {
	cache, _ := loadDescriptorCache(ctx, idx)

	if cache != nil {
		want, err := descriptor.Checksum(canonical)
		if err != nil {
			return fmt.Errorf("checksum canonical: %w", err)
		}
		if bytes.Equal(cache.Checksum, want) {
			return nil // cache is already current
		}
	}

	// Save (or re-save). saveDescriptorCache is idempotent.
	if err := saveDescriptorCache(ctx, idx, canonical); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	return nil
}

// buildStore is the common tail shared by InitStore and OpenStore.
// It constructs the *store value, runs the bootstrap Orphan Scan,
// publishes the report, and transitions the Store into
// StateUnlocked. Errors are surfaced unwrapped — the caller adds
// its own "core.InitStore" / "core.OpenStore" prefix.
//
// Pre-conditions checked by the caller (not re-checked here):
//   - drv != nil
//   - idx != nil
//   - cfg has been defaulted and validated
//   - storeID is fresh (Init) or read from the descriptor (Open)
//
// When M2 lands the Locked → Bootstrapping → Unlocked transition
// (encrypted Stores), this helper is the single point that learns
// to wait for Unlock before flipping the state — both entry
// points then pick up the new flow without further changes.
func buildStore(
	ctx context.Context,
	o storeOptions,
	drv driver.Driver,
	idx StoreIndex,
	cfg domain.StoreConfig,
	desc *descriptor.Descriptor,
	dek []byte,
) (*store, error) {
	_ = ctx // reserved for future bootstrap-time index probes
	s := &store{
		storeID:            desc.StoreID,
		drv:                drv,
		index:              idx,
		pub:                o.publisher,
		activeConfig:       cfg,
		state:              domain.StateBootstrapping,
		hashes:             o.hashRegistry,
		transformers:       o.readRegistry,
		keyResolver:        o.keyResolver,
		capabilityToken:    o.capabilityToken,
		desc:               desc,
		dek:                dek,
		passphraseProvider: o.passphrase,
	}
	return s, nil
}

// unlockBootstrap completes the bootstrap-into-Unlocked transition
// shared by InitStore (always), the Plain-DEK OpenStore path,
// the AutoUnlock OpenStore path, and the deferred Store.Unlock
// path (1.2b.5).
//
// The caller has produced a *store in StateBootstrapping with
// the DEK already populated. unlockBootstrap runs the Orphan
// Scan per §10.2, publishes the report, and flips state to
// StateUnlocked atomically.
//
// Errors from the Orphan Scan propagate; the *store is left in
// StateBootstrapping. The caller decides whether to retry, fall
// back to Locked, or surface the failure.
func unlockBootstrap(ctx context.Context, s *store, pub Publisher) error {
	report, err := recoverOrphans(ctx, s.drv, s.index)
	if err != nil {
		return fmt.Errorf("orphan scan: %w", err)
	}
	// Record the scan timestamp per docs/2 §10.2 "Label". Best-effort:
	// SetMeta failure is appended to the report so observability sees
	// it, but does not block the transition — the cache key is a
	// diagnostic aid, not a liveness gate.
	if setErr := s.index.SetMeta(ctx, "last_orphan_scan_at", time.Now().UTC().Format(time.RFC3339)); setErr != nil {
		report.Errors = append(report.Errors,
			fmt.Errorf("unlockBootstrap: persist last_orphan_scan_at: %w", setErr))
	}
	publishOrphanReport(pub, report)

	s.stateMu.Lock()
	s.state = domain.StateUnlocked
	s.stateMu.Unlock()
	return nil
}

// validateAgainstActiveConfig checks that the caller-supplied
// StoreConfig agrees with the active system.config on every
// immutable parameter. Mutable parameters are not compared — they
// are reassignable through UpdateConfig (M2+).
//
// Only fields the caller actually populated (non-zero values in the
// requested config) are compared; a caller who passes WithConfig{}
// or partial WithConfig with only mutable fields passes through.
// A caller who passes an immutable that does not match the active
// config gets errs.ErrConfigMismatch.
//
// Rationale for "non-zero comparison": go zero values are
// indistinguishable from "field omitted". The caller can always
// pass an explicit value to opt into the check; a default value
// passes silently. This matches the contract documented in
// 4. API Reference/01 Lifecycle §1.2.
func validateAgainstActiveConfig(req, active domain.StoreConfig) error {
	var mismatches []string

	if req.PathTopology != "" && req.PathTopology != active.PathTopology {
		mismatches = append(mismatches,
			fmt.Sprintf("PathTopology: requested %q, active %q",
				req.PathTopology, active.PathTopology))
	}
	if req.ManifestStorage != "" && req.ManifestStorage != active.ManifestStorage {
		mismatches = append(mismatches,
			fmt.Sprintf("ManifestStorage: requested %q, active %q",
				req.ManifestStorage, active.ManifestStorage))
	}
	if req.ManifestEncoding != "" && req.ManifestEncoding != active.ManifestEncoding {
		mismatches = append(mismatches,
			fmt.Sprintf("ManifestEncoding: requested %q, active %q",
				req.ManifestEncoding, active.ManifestEncoding))
	}
	if req.ManifestCrypto != "" && req.ManifestCrypto != active.ManifestCrypto {
		mismatches = append(mismatches,
			fmt.Sprintf("ManifestCrypto: requested %q, active %q",
				req.ManifestCrypto, active.ManifestCrypto))
	}
	if req.ContentHasher != "" && req.ContentHasher != active.ContentHasher {
		mismatches = append(mismatches,
			fmt.Sprintf("ContentHasher: requested %q, active %q",
				req.ContentHasher, active.ContentHasher))
	}
	// DeletionPolicyLock: bool, "not set" indistinguishable from
	// "false". Compare only when the caller explicitly asked to
	// lock — false is the relaxed default and passing it should not
	// fail against a locked active config.
	if req.DeletionPolicyLock && !active.DeletionPolicyLock {
		mismatches = append(mismatches,
			"DeletionPolicyLock: requested true, active false")
	}

	if len(mismatches) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", errs.ErrConfigMismatch, strings.Join(mismatches, "; "))
}
