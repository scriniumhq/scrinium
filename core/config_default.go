package core

import (
	"time"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
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
	// MetadataTransformer, Pipeline, KDFParams: zero values are
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
	case domain.ManifestEncodingJSON, domain.ManifestEncodingBinary:
	default:
		return errs.ErrInvalidConfig
	}
	switch cfg.ManifestCrypto {
	case domain.ManifestCryptoPlain, domain.ManifestCryptoMetadataOnly, domain.ManifestCryptoEnvelope:
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
	if cfg.TombstoneGracePeriod > 0 && cfg.TombstoneGracePeriod < time.Hour {
		return errs.ErrInvalidTombstoneGracePeriod
	}
	return nil
}
