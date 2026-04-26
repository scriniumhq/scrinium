package core

import "time"

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
func applyConfigDefaults(cfg StoreConfig) StoreConfig {
	if cfg.PathTopology == "" {
		cfg.PathTopology = PathTopologySharded
	}
	if cfg.ManifestStorage == "" {
		cfg.ManifestStorage = ManifestStorageRemote
	}
	if cfg.BlobStorage == "" {
		cfg.BlobStorage = BlobStorageTarget
	}
	if cfg.ManifestEncoding == "" {
		cfg.ManifestEncoding = ManifestEncodingJSON
	}
	if cfg.ManifestCrypto == "" {
		cfg.ManifestCrypto = ManifestCryptoPlain
	}
	if cfg.ContentHasher == "" {
		cfg.ContentHasher = HashSHA256
	}
	if cfg.VerifyOnRead == "" {
		cfg.VerifyOnRead = VerifyOnReadAuto
	}
	if cfg.DeletionPolicy == "" {
		cfg.DeletionPolicy = DeletionPolicyFree
	}
	if cfg.GCLeasePolicy == "" {
		cfg.GCLeasePolicy = GCLeaseAuto
	}
	if cfg.PackAlignment == 0 {
		// Zero literal in Go for an int-typed enum is also
		// PackAlignmentNone. We disambiguate "user wanted None"
		// from "user left it zero" by promoting zero to Auto: most
		// callers want the engine to derive alignment from the
		// Driver's capabilities, not "no alignment whatsoever".
		cfg.PackAlignment = PackAlignmentAuto
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
func validateImmutableConfig(cfg StoreConfig) error {
	switch cfg.PathTopology {
	case PathTopologyFlat, PathTopologySharded, PathTopologyNative:
	default:
		return ErrInvalidConfig
	}
	switch cfg.ManifestStorage {
	case ManifestStorageRemote, ManifestStorageLocal, ManifestStorageReplicated:
	default:
		return ErrInvalidConfig
	}
	switch cfg.ManifestEncoding {
	case ManifestEncodingJSON, ManifestEncodingBinary:
	default:
		return ErrInvalidConfig
	}
	switch cfg.ManifestCrypto {
	case ManifestCryptoPlain, ManifestCryptoMetadataOnly, ManifestCryptoEnvelope:
	default:
		return ErrInvalidConfig
	}
	switch cfg.ContentHasher {
	case HashSHA256, HashBLAKE3:
	default:
		return ErrInvalidConfig
	}

	// PathTopology: Native is a read-only marker; allowed only
	// with BlobStorage: ExternalRef.
	if cfg.PathTopology == PathTopologyNative &&
		cfg.BlobStorage != BlobStorageExternalRef {
		return ErrInvalidConfig
	}

	// TombstoneGracePeriod has its own dedicated sentinel because
	// a too-short value is the only param with cross-host safety
	// implications. The minimum below matches docs/4 §5.1.
	if cfg.TombstoneGracePeriod > 0 && cfg.TombstoneGracePeriod < time.Hour {
		return ErrInvalidTombstoneGracePeriod
	}
	return nil
}