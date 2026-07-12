package config

import (
	"fmt"
	"strings"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// ApplyDefaults fills in zero-valued StoreConfig fields with sensible
// defaults. Called once at InitStore before serialising the
// descriptor, so the immutable parameters chosen here are fixed for
// the lifetime of the Store.
//
// Mutable parameters get defaults too: this matters when callers pass
// an empty StoreConfig{} just to take the engine's recommendation.
//
// The defaults reflect the "embeddable POSIX local backend" scenario:
// hashed paths for big trees, no encryption, JSON manifests, immediate
// deletion. Non-default scenarios (S3, encrypted, WORM) require
// explicit StoreConfig overrides.
func ApplyDefaults(cfg domain.StoreConfig) domain.StoreConfig {
	if cfg.PathTopology == "" {
		cfg.PathTopology = domain.PathTopologySharded
	}
	if cfg.BlobStorage == "" {
		cfg.BlobStorage = domain.BlobStorageTarget
	}
	if cfg.ManifestEncoding == "" {
		cfg.ManifestEncoding = domain.ManifestEncodingJSON
	}
	if cfg.ManifestCrypto == "" {
		cfg.ManifestCrypto = domain.ManifestCryptoPlain
	}
	// ADR-58: an encrypting store defaults to Disabled (no dedup of
	// encrypted blobs, full AEAD semantics). A Plain store leaves the
	// field empty — it is ignored there (crypto-identity is empty, the
	// dedup key degrades to (ContentHash, OriginalSize)).
	if cfg.EncryptedDedup == "" && isEncryptingConfig(cfg) {
		cfg.EncryptedDedup = domain.EncryptedDedupDisabled
	}
	// ADR-59: the segmented AEAD blob format needs a segment size for
	// any encrypting store. Mirror EncryptedDedup — default it only
	// when the store actually encrypts; a Plain store leaves it zero
	// (no crypto stage ever reads it). Immutable once chosen.
	if cfg.SegmentSize == 0 && isEncryptingConfig(cfg) {
		cfg.SegmentSize = domain.DefaultSegmentSize
	}
	if cfg.ContentHasher == "" {
		cfg.ContentHasher = domain.HashSHA256
	}
	if cfg.VerifyOnRead == "" {
		cfg.VerifyOnRead = domain.VerifyOnReadAuto
	}
	if cfg.DeletionPolicy == "" {
		cfg.DeletionPolicy = domain.DeletionPolicyFree
	}
	if cfg.GCLeasePolicy == "" {
		cfg.GCLeasePolicy = domain.GCLeaseAuto
	}
	if cfg.SessionOverrides == "" {
		cfg.SessionOverrides = domain.SessionOverridesAllow
	}
	if cfg.PackAlignment == 0 {
		// Zero literal in Go for an int-typed enum is also
		// PackAlignmentNone. We disambiguate "user wanted None" from
		// "user left it zero" by promoting zero to Auto: most callers
		// want the engine to derive alignment from the Driver's
		// capabilities, not "no alignment whatsoever".
		cfg.PackAlignment = domain.PackAlignmentAuto
	}
	if cfg.TombstoneGracePeriod == 0 {
		cfg.TombstoneGracePeriod = 24 * time.Hour
	}
	// InlineBlobLimit, RetentionPeriod, EagerFetchLimit, Pipeline,
	// KDFParams: zero values are legitimate "feature off" or "use
	// plugin defaults" signals. We do NOT override them.
	return cfg
}

// ValidateImmutable checks the immutable subset of StoreConfig for
// impossible combinations the Rules Engine would reject. This is the
// M1 subset; the full Rules Engine (cross-field, cross-store
// validation) lands in M4.
//
// The set of checks here is deliberately small. We catch the obvious
// mistakes — empty enum, contradictory hashes — and let later
// milestones add the full matrix.
// ValidateImmutable checks every StoreConfig field's value against its
// enum / bounds. It is the single gate on BOTH the init and update
// paths (despite the historical name — it validates all fields, not
// only class I; mutable enums reach store.config through UpdateConfig
// and must be gated too, R-a).
//
// The per-field rules live once, in the registry (registry.go); this
// loops it. A field with no value-level rule (a bool, an unbounded
// int) carries a nil Check and passes. First failure wins; field order
// follows the struct.
func ValidateImmutable(cfg domain.StoreConfig) error {
	for _, f := range registry {
		if err := f.validate(cfg); err != nil {
			return err
		}
	}
	return nil
}

// ValidateAgainstActive compares a requested config to the currently
// active one on every immutable field; mutable fields pass through.
// Used by OpenStore's WithConfig check and by UpdateConfig.
//
// Only fields the caller actually populated (non-zero values in the
// requested config) are compared; a caller who passes WithConfig{} or
// a partial WithConfig with only mutable fields passes through. A
// caller who passes an immutable that does not match the active config
// gets errs.ErrConfigMismatch.
//
// Rationale for "non-zero comparison": Go zero values are
// indistinguishable from "field omitted". The caller can always pass
// an explicit value to opt into the check; a default value passes
// silently.
// ValidateAgainstActive compares a requested config to the active one
// on every IMMUTABLE (class I) field; mutable fields pass through.
// Used by OpenStore's WithConfig check and by UpdateConfig. Derived
// from the registry filtered to class I — the same field list, the
// same diverges rule (populated-and-different), no separate hand list.
//
// Only fields the caller populated (non-zero) are compared: a Go zero
// is indistinguishable from "omitted", so a caller opts into the check
// by passing an explicit value. DeletionPolicyLock (a bool) rides this
// exactly: false is the relaxed default and reads as unset, so passing
// false never fails against a locked active config; only an explicit
// true-vs-false diverges.
func ValidateAgainstActive(req, active domain.StoreConfig) error {
	mismatches := divergentByClass(ClassImmutable, req, active)
	if len(mismatches) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", errs.ErrConfigMismatch, strings.Join(mismatches, "; "))
}

// isEncryptingConfig reports whether the config produces encrypted
// blobs — either the manifest body is protected (Sealed/Paranoid) or
// the blob Pipeline contains a crypto stage. EncryptedDedup and
// SegmentSize only have meaning for such stores. The Pipeline-stage
// check is name-based against the registered crypto algorithms;
// it stays correct as long as crypto plugins register under their
// canonical ids.
func isEncryptingConfig(cfg domain.StoreConfig) bool {
	if cfg.ManifestCrypto != "" && cfg.ManifestCrypto != domain.ManifestCryptoPlain {
		return true
	}
	for _, algo := range cfg.Pipeline {
		if domain.IsCryptoAlgorithm(algo) {
			return true
		}
	}
	return false
}
