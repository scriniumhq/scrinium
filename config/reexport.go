package config

import "scrinium.dev/config/internal/storeconfig"

// Re-export of the store-configuration model. The types and their enum
// values are declared in config/internal/store (kept internal so the
// package is not imported directly from outside); this file is the
// public face — every external caller refers to them as config.X.
//
// The field registry, validation, defaults and connection logic in this
// package operate on these same types. Anything configurable about a
// store is named here.

// StoreConfig is the full store configuration.
type StoreConfig = storeconfig.StoreConfig

// Enum and parameter types.
type (
	PathTopology           = storeconfig.PathTopology
	BlobStorage            = storeconfig.BlobStorage
	ManifestEncoding       = storeconfig.ManifestEncoding
	ManifestCrypto         = storeconfig.ManifestCrypto
	EncryptedDedup         = storeconfig.EncryptedDedup
	DeletionPolicy         = storeconfig.DeletionPolicy
	SessionOverridesPolicy = storeconfig.SessionOverridesPolicy
	GCLeasePolicy          = storeconfig.GCLeasePolicy
	PackAlignmentPolicy    = storeconfig.PackAlignmentPolicy
	VerifyOnReadPolicy     = storeconfig.VerifyOnReadPolicy
	ContentHashAlgorithm   = storeconfig.ContentHashAlgorithm
	IdentityMode           = storeconfig.IdentityMode
	KDFParams              = storeconfig.KDFParams
)

// Enum values.
const (
	PathTopologyFlat    = storeconfig.PathTopologyFlat
	PathTopologySharded = storeconfig.PathTopologySharded

	BlobStorageTarget = storeconfig.BlobStorageTarget
	BlobStorageInline = storeconfig.BlobStorageInline

	ManifestEncodingJSON   = storeconfig.ManifestEncodingJSON
	ManifestEncodingBinary = storeconfig.ManifestEncodingBinary

	ManifestCryptoPlain    = storeconfig.ManifestCryptoPlain
	ManifestCryptoSealed   = storeconfig.ManifestCryptoSealed
	ManifestCryptoParanoid = storeconfig.ManifestCryptoParanoid

	EncryptedDedupDisabled   = storeconfig.EncryptedDedupDisabled
	EncryptedDedupConvergent = storeconfig.EncryptedDedupConvergent

	DeletionPolicyNoDelete  = storeconfig.DeletionPolicyNoDelete
	DeletionPolicyRetention = storeconfig.DeletionPolicyRetention
	DeletionPolicyFree      = storeconfig.DeletionPolicyFree

	SessionOverridesAllow = storeconfig.SessionOverridesAllow
	SessionOverridesDeny  = storeconfig.SessionOverridesDeny

	GCLeaseAuto           = storeconfig.GCLeaseAuto
	GCLeaseSingleHost     = storeconfig.GCLeaseSingleHost
	GCLeaseLeaderElection = storeconfig.GCLeaseLeaderElection

	PackAlignmentAuto = storeconfig.PackAlignmentAuto
	PackAlignmentNone = storeconfig.PackAlignmentNone
	PackAlignment512  = storeconfig.PackAlignment512
	PackAlignment4096 = storeconfig.PackAlignment4096

	VerifyOnReadAuto         = storeconfig.VerifyOnReadAuto
	VerifyOnReadForceEnabled = storeconfig.VerifyOnReadForceEnabled
	VerifyOnReadDisabled     = storeconfig.VerifyOnReadDisabled

	HashSHA256 = storeconfig.HashSHA256
	HashBLAKE3 = storeconfig.HashBLAKE3

	IdentityModeUnique    = storeconfig.IdentityModeUnique
	IdentityModeCoalesced = storeconfig.IdentityModeCoalesced
)
