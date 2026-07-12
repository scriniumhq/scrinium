package config

import (
	"scrinium.dev/config/internal/fieldkit"
	"scrinium.dev/domain"
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
// ApplyDefaults fills zero-valued StoreConfig fields with their
// defaults. Each field's default lives in its registry row (registry.go)
// — an unconditional value (DefaultTo) or a function of the whole config
// for conditional / zero-is-meaningful cases (DefaultFn). This loops
// the registry; there is no separate hand-written default list.
//
// Order matters for the conditional defaults: EncryptedDedup and
// SegmentSize key off isEncryptingConfig(cfg), which reads
// ManifestCrypto and Pipeline — neither is mutated by defaulting, so a
// single forward pass is correct regardless of field order.
func ApplyDefaults(cfg domain.StoreConfig) domain.StoreConfig {
	return fieldkit.ApplyDefaults(registry, cfg)
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
	return fieldkit.ValidateAll(registry, cfg)
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
	return fieldkit.MismatchAgainstActive(registry, req, active)
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
