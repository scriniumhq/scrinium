package storeconfig

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
func ValidateImmutable(cfg domain.StoreConfig) error {
	switch cfg.PathTopology {
	case domain.PathTopologyFlat, domain.PathTopologySharded:
	default:
		return errs.ErrInvalidConfig
	}
	switch cfg.ManifestEncoding {
	case domain.ManifestEncodingJSON:
		// OK
	case domain.ManifestEncodingBinary:
		// Binary (\x00SC2 / MsgPack) magic is reserved by the format
		// and recognised by manifestcodec, but the deterministic-
		// encode side is not yet shipped — see 7. Planning/backlog.md
		// §3.3 "Бинарные манифесты (MsgPack)". Refuse loudly at
		// InitStore rather than crash on the first user-level Put with
		// ErrUnsupportedEncoding from manifestcodec.
		return fmt.Errorf("%w: ManifestEncoding=Binary deferred (see backlog §3.3)",
			errs.ErrInvalidConfig)
	default:
		return errs.ErrInvalidConfig
	}
	switch cfg.ManifestCrypto {
	case domain.ManifestCryptoPlain, domain.ManifestCryptoSealed, domain.ManifestCryptoParanoid:
	default:
		return errs.ErrInvalidConfig
	}
	switch cfg.ContentHasher {
	case domain.HashSHA256, domain.HashBLAKE3:
	default:
		return errs.ErrInvalidConfig
	}
	// ADR-58: EncryptedDedup is immutable and constrained to the known
	// modes. "" is legitimate for a Plain store — the field is ignored
	// there.
	switch cfg.EncryptedDedup {
	case "", domain.EncryptedDedupDisabled, domain.EncryptedDedupConvergent:
	default:
		return errs.ErrInvalidConfig
	}

	// ADR-59: SegmentSize is immutable. Zero is legitimate (Plain
	// store, or a not-yet-defaulted config); a non-zero value must
	// fall within the format's bounds.
	if cfg.SegmentSize != 0 &&
		(cfg.SegmentSize < domain.MinSegmentSize || cfg.SegmentSize > domain.MaxSegmentSize) {
		return fmt.Errorf("%w: SegmentSize=%d out of range [%d, %d]",
			errs.ErrInvalidConfig, cfg.SegmentSize, domain.MinSegmentSize, domain.MaxSegmentSize)
	}

	// TombstoneGracePeriod has its own dedicated sentinel because a
	// too-short value is the only param with cross-host safety
	// implications. The minimum below matches docs/4 §5.1.
	if cfg.TombstoneGracePeriod > 0 && cfg.TombstoneGracePeriod < domain.MinTombstoneGracePeriod {
		return errs.ErrInvalidTombstoneGracePeriod
	}

	// InlineBlobLimit upper bound: bigger inline blobs would push hot
	// index pages out of SQLite's page cache. Zero means "feature off"
	// — only positive values are constrained.
	if cfg.InlineBlobLimit > 0 && cfg.InlineBlobLimit > domain.MaxInlineBlobLimit {
		return fmt.Errorf("%w: InlineBlobLimit=%d exceeds %d",
			errs.ErrInvalidConfig, cfg.InlineBlobLimit, domain.MaxInlineBlobLimit)
	}

	// RetentionPeriod lower bound: a shorter window than the GC cycle
	// defeats the purpose. Zero means "feature off".
	if cfg.RetentionPeriod > 0 && cfg.RetentionPeriod < domain.MinRetentionPeriod {
		return fmt.Errorf("%w: RetentionPeriod=%v shorter than %v",
			errs.ErrInvalidConfig, cfg.RetentionPeriod, domain.MinRetentionPeriod)
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
// silently. This matches the contract documented in
// 4. API Reference/01 Lifecycle §1.2.
func ValidateAgainstActive(req, active domain.StoreConfig) error {
	var mismatches []string

	if req.PathTopology != "" && req.PathTopology != active.PathTopology {
		mismatches = append(mismatches,
			fmt.Sprintf("PathTopology: requested %q, active %q",
				req.PathTopology, active.PathTopology))
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
	// ADR-58: EncryptedDedup is immutable — changing it would break
	// reproducibility of historical encrypted blob addresses.
	if req.EncryptedDedup != "" && req.EncryptedDedup != active.EncryptedDedup {
		mismatches = append(mismatches,
			fmt.Sprintf("EncryptedDedup: requested %q, active %q",
				req.EncryptedDedup, active.EncryptedDedup))
	}
	// ADR-59: SegmentSize is immutable — changing it would break
	// ciphertext reproducibility under Convergent (and therefore dedup
	// of encrypted blobs/chunks).
	if req.SegmentSize != 0 && req.SegmentSize != active.SegmentSize {
		mismatches = append(mismatches,
			fmt.Sprintf("SegmentSize: requested %d, active %d",
				req.SegmentSize, active.SegmentSize))
	}
	if req.ContentHasher != "" && req.ContentHasher != active.ContentHasher {
		mismatches = append(mismatches,
			fmt.Sprintf("ContentHasher: requested %q, active %q",
				req.ContentHasher, active.ContentHasher))
	}
	// DeletionPolicyLock: bool, "not set" indistinguishable from
	// "false". Compare only when the caller explicitly asked to lock —
	// false is the relaxed default and passing it should not fail
	// against a locked active config.
	if req.DeletionPolicyLock && !active.DeletionPolicyLock {
		mismatches = append(mismatches,
			"DeletionPolicyLock: requested true, active false")
	}

	if len(mismatches) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", errs.ErrConfigMismatch, strings.Join(mismatches, "; "))
}

// isEncryptingConfig reports whether the config produces encrypted
// blobs — either the manifest body is protected (Sealed/Paranoid) or
// the blob Pipeline contains a crypto stage. EncryptedDedup and
// SegmentSize only have meaning for such stores. The Pipeline-stage
// check is name-based against the registered crypto algorithms
// (3. Reference/04 §4.3); it stays correct as long as crypto plugins
// register under their canonical ids.
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
