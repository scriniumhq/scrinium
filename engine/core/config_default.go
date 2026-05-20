package core

import (
	"fmt"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
)

// applyConfigDefaults fills in zero-valued StoreConfig fields with
// sensible defaults. Called once at InitStore before serialising
// the descriptor, so the immutable parameters chosen here are
// fixed for the lifetime of the Store.
//
// Mutable parameters get defaults too: this matters when callers
// pass an empty StoreConfig{} just to take the engine's
// recommendation.
//
// The defaults reflect the "embeddable POSIX local backend"
// scenario: hashed paths for big trees, no encryption, JSON
// manifests, immediate deletion. Non-default scenarios (S3,
// encrypted, WORM) require explicit StoreConfig overrides.
func applyConfigDefaults(cfg domain.StoreConfig) domain.StoreConfig {
	if cfg.PathTopology == "" {
		cfg.PathTopology = domain.PathTopologySharded
	}
	if cfg.ManifestStorage == "" {
		cfg.ManifestStorage = domain.ManifestStorageRemote
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
		// PackAlignmentNone. We disambiguate "user wanted None"
		// from "user left it zero" by promoting zero to Auto: most
		// callers want the engine to derive alignment from the
		// Driver's capabilities, not "no alignment whatsoever".
		cfg.PackAlignment = domain.PackAlignmentAuto
	}
	if cfg.TombstoneGracePeriod == 0 {
		cfg.TombstoneGracePeriod = 24 * time.Hour
	}
	// InlineBlobLimit, RetentionPeriod, EagerFetchLimit,
	// Pipeline, KDFParams: zero values are
	// legitimate "feature off" or "use plugin defaults" signals.
	// We do NOT override them.
	return cfg
}

// validateImmutableConfig checks the immutable subset of StoreConfig
// for impossible combinations the Rules Engine would reject. This
// is the M1 subset; the full Rules Engine (cross-field, cross-store
// validation) lands in M4.
//
// The set of checks here is deliberately small. We catch the
// obvious mistakes — empty enum, contradictory hashes — and let
// later milestones add the full matrix.
func validateImmutableConfig(cfg domain.StoreConfig) error {
	switch cfg.PathTopology {
	case domain.PathTopologyFlat, domain.PathTopologySharded, domain.PathTopologyNative:
	default:
		return errs.ErrInvalidConfig
	}
	switch cfg.ManifestStorage {
	case domain.ManifestStorageRemote, domain.ManifestStorageLocal, domain.ManifestStorageReplicated:
	default:
		return errs.ErrInvalidConfig
	}
	switch cfg.ManifestEncoding {
	case domain.ManifestEncodingJSON:
		// OK
	case domain.ManifestEncodingBinary:
		// Binary (\x00SC2 / MsgPack) magic is reserved by the
		// format and recognised by manifestcodec, but the
		// deterministic-encode side is not yet shipped — see
		// 7. Planning/backlog.md §3.3 "Бинарные манифесты
		// (MsgPack)". Refuse loudly at InitStore rather than
		// crash on the first user-level Put with
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

	// PathTopology: Native is a read-only marker; allowed only
	// with BlobStorage: ExternalRef.
	if cfg.PathTopology == domain.PathTopologyNative &&
		cfg.BlobStorage != domain.BlobStorageExternalRef {
		return errs.ErrInvalidConfig
	}

	// TombstoneGracePeriod has its own dedicated sentinel because
	// a too-short value is the only param with cross-host safety
	// implications. The minimum below matches docs/4 §5.1.
	if cfg.TombstoneGracePeriod > 0 && cfg.TombstoneGracePeriod < domain.MinTombstoneGracePeriod {
		return errs.ErrInvalidTombstoneGracePeriod
	}

	// InlineBlobLimit upper bound: bigger inline blobs would push
	// hot index pages out of SQLite's page cache. Zero means "feature
	// off" — only positive values are constrained.
	if cfg.InlineBlobLimit > 0 && cfg.InlineBlobLimit > domain.MaxInlineBlobLimit {
		return fmt.Errorf("%w: InlineBlobLimit=%d exceeds %d",
			errs.ErrInvalidConfig, cfg.InlineBlobLimit, domain.MaxInlineBlobLimit)
	}

	// RetentionPeriod lower bound: a shorter window than the GC
	// cycle defeats the purpose. Zero means "feature off".
	if cfg.RetentionPeriod > 0 && cfg.RetentionPeriod < domain.MinRetentionPeriod {
		return fmt.Errorf("%w: RetentionPeriod=%v shorter than %v",
			errs.ErrInvalidConfig, cfg.RetentionPeriod, domain.MinRetentionPeriod)
	}

	return nil
}
