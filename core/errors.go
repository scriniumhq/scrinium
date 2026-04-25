package core

import "errors"

// All errors are identified through errors.Is. Some wrap an
// underlying cause (PassphraseProvider, ArchivedArtifact for backup
// sources) and support errors.Unwrap.

// --- Store states and bootstrap ---

// ErrStoreNotReady — the Store is in StateBootstrapping. The API
// is blocked until initialisation completes.
var ErrStoreNotReady = errors.New("core: store not ready")

// ErrStoreNotFound — OpenStore: no store.json in the Location.
// Not to be confused with ErrArtifactNotFound (an artifact inside
// an open Store).
var ErrStoreNotFound = errors.New("core: store not found")

// ErrStoreAlreadyExists — InitStore without WithForceReinit on top
// of an existing Store.
var ErrStoreAlreadyExists = errors.New("core: store already exists")

// ErrStoreCorrupted — every descriptor replica is corrupted, or
// the StoreIndex is corrupted. The Store is in StateCorrupted.
var ErrStoreCorrupted = errors.New("core: store corrupted")

// ErrLocked — the operation was invoked in StateLocked. Unlock is
// required.
var ErrLocked = errors.New("core: store locked")

// ErrInvalidKey — the KEK does not decrypt the DEK: wrong password
// or corrupted EncryptedDEK.
var ErrInvalidKey = errors.New("core: invalid key")

// ErrStoreReadOnly — MaintenanceModeReadOnly + a mutating operation.
var ErrStoreReadOnly = errors.New("core: store read-only")

// ErrStoreOffline — MaintenanceModeOffline.
var ErrStoreOffline = errors.New("core: store offline")

// ErrSharedIndexRequired — OpenStore on the SQLite backend has
// found a live foreign location.lock; a shared backend (Postgres)
// or a clean shutdown of the other process is required.
var ErrSharedIndexRequired = errors.New("core: shared index required")

// ErrManifestsLost — RebuildIndexAgent did not find any manifests:
// ManifestStorage: Local with the local disk lost and no
// HostStorage available.
var ErrManifestsLost = errors.New("core: manifests lost")

// ErrHostStorageLocked — Curator startup aborted: the WorkspaceDir
// of the transit buffer is held by another process (OS-level lock).
var ErrHostStorageLocked = errors.New("core: host storage locked by another process")

// --- Configuration pointer (system.config/current) ---

// ErrMissingConfigPointer — the pointer file is absent.
var ErrMissingConfigPointer = errors.New("core: missing config pointer")

// ErrCorruptedConfigPointer — the pointer exists but is invalid.
var ErrCorruptedConfigPointer = errors.New("core: corrupted config pointer")

// ErrDanglingConfigPointer — the pointer is syntactically valid
// but the artifact does not exist.
var ErrDanglingConfigPointer = errors.New("core: dangling config pointer")

// ErrConfigMismatch — an attempt to change an immutable parameter
// through UpdateConfig, or a conflict between the cfg passed to
// OpenStore and the configuration loaded from
// system.config/current, or an attempt to remove NoDelete while
// DeletionPolicyLock is in effect.
var ErrConfigMismatch = errors.New("core: config mismatch")

// --- Passphrase and recovery ---

// ErrPassphraseRequired — the operation needs a KEK but
// WithPassphrase was not configured. Also returned by
// ExportRecoveryKit on a ManifestCrypto: Plain Store.
var ErrPassphraseRequired = errors.New("core: passphrase required")

// ErrPassphraseProvider — the provider returned an error. Wraps
// the underlying cause (available through errors.Unwrap).
var ErrPassphraseProvider = errors.New("core: passphrase provider error")

// ErrRecoveryKitCorrupted — the Recovery Kit is corrupted (the
// checksum does not match).
var ErrRecoveryKitCorrupted = errors.New("core: recovery kit corrupted")

// ErrInvalidRecoveryKey — failed to decrypt the DEK from the
// Recovery Kit.
var ErrInvalidRecoveryKey = errors.New("core: invalid recovery key")

// ErrInvalidKDFParams — InitStore with KDFParams that fail the
// minimum-validity check: Time < 1, Memory < 19456 KiB, Threads < 1.
var ErrInvalidKDFParams = errors.New("core: invalid KDF params")

// --- Encryption and key resolution ---

// ErrKeyNotFound — the KeyResolver does not know the key for the
// requested KeyID.
var ErrKeyNotFound = errors.New("core: key not found")

// ErrDecryptionFailed — the key was found but decryption failed
// (wrong key, corrupted bytes, authentication-tag failure).
var ErrDecryptionFailed = errors.New("core: decryption failed")

// --- Validation and limits ---

// ErrInvalidConfig — a StoreConfig parameter is out of range or
// violates the Rules Engine.
var ErrInvalidConfig = errors.New("core: invalid config")

// ErrInvalidTombstoneGracePeriod — TombstoneGracePeriod < 1h. A
// dedicated sentinel: this is the only parameter with runtime
// implications for multi-host safety.
var ErrInvalidTombstoneGracePeriod = errors.New("core: invalid tombstone grace period")

// ErrNamespaceTooLong — namespace > 255 bytes.
var ErrNamespaceTooLong = errors.New("core: namespace too long")

// ErrSessionIDTooLong — SessionID > 255 bytes.
var ErrSessionIDTooLong = errors.New("core: session ID too long")

// ErrEmptySessionID — RollbackSession called with an empty string;
// guards against a mass deletion of sessionless artifacts.
var ErrEmptySessionID = errors.New("core: empty session ID")

// ErrMetadataTooLarge — Artifact.Metadata > 64 KB.
var ErrMetadataTooLarge = errors.New("core: metadata too large")

// ErrManifestTooLarge — serialised Manifest > 1 MB.
var ErrManifestTooLarge = errors.New("core: manifest too large")

// ErrReservedNamespace — an attempt to use "*" or the "system."
// prefix without a CapabilityToken.
var ErrReservedNamespace = errors.New("core: reserved namespace")

// --- Artifacts, deletion, and retention ---

// ErrArtifactNotFound — no manifest with the given ArtifactID
// exists in the Store, or it is a ManifestTypePack (an internal
// type that does not exist for the client).
var ErrArtifactNotFound = errors.New("core: artifact not found")

// ErrDeletionForbidden — Delete on a Store with
// DeletionPolicy: NoDelete.
var ErrDeletionForbidden = errors.New("core: deletion forbidden")

// ErrRetentionNotExpired — Delete or RollbackSession on an artifact
// with an active RetentionUntil.
var ErrRetentionNotExpired = errors.New("core: retention not expired")

// ErrArchivedArtifact — the artifact is reachable only through a
// Backup with ReadPolicy: Never; AllowColdRead is required.
var ErrArchivedArtifact = errors.New("core: archived artifact")

// --- Verification and integrity ---

// ErrCorruptedManifest — the hash of the manifest file does not
// match its ArtifactID.
var ErrCorruptedManifest = errors.New("core: corrupted manifest")

// ErrCorruptedBlob — the hash of the physical blob does not match
// its BlobRef.
var ErrCorruptedBlob = errors.New("core: corrupted blob")

// ErrCorruptedContent — after the inverse Pipeline the hash does
// not match ContentHash.
var ErrCorruptedContent = errors.New("core: corrupted content")

// --- Formats and compatibility ---

// ErrUnsupportedSchemaVersion — the manifest's schema_version is
// not supported.
var ErrUnsupportedSchemaVersion = errors.New("core: unsupported schema version")

// ErrUnknownPackFormat — the magic bytes of a .pack volume are
// unrecognised.
var ErrUnknownPackFormat = errors.New("core: unknown pack format")

// ErrUnsupportedURIScheme — the driver does not support the URI
// scheme. Shared sentinel with driver.ErrUnsupportedURIScheme.
var ErrUnsupportedURIScheme = errors.New("core: unsupported URI scheme")

// ErrRandomAccessNotSupported — ReadAt/ReadAtCtx was called on a
// stream that does not support random access.
var ErrRandomAccessNotSupported = errors.New("core: random access not supported")

// --- Maintenance and index ---

// ErrIndexCorrupted — the StoreIndex is missing or its checksum
// does not match.
var ErrIndexCorrupted = errors.New("core: index corrupted")

// ErrIndexSchemaMismatch — the StoreIndex schema version is
// incompatible.
var ErrIndexSchemaMismatch = errors.New("core: index schema mismatch")

// ErrMaintenanceInProgress — another Maintenance Agent holds the
// lease.
var ErrMaintenanceInProgress = errors.New("core: maintenance in progress")

// ErrMetaKeyNotFound — the requested key is missing in store_meta.
var ErrMetaKeyNotFound = errors.New("core: meta key not found")

// --- Walk control ---

// ErrStopWalk — the callback for Walk/WalkSystem returns this
// sentinel for an early but successful exit.
var ErrStopWalk = errors.New("core: stop walk")

// --- Leases and locks ---

// ErrLeaseHeld — an attempt to acquire a lease held by an active
// owner.
var ErrLeaseHeld = errors.New("core: lease held")

// ErrLeaseLost — the lease was lost in flight or right after a
// takeover (concurrent steal).
var ErrLeaseLost = errors.New("core: lease lost")
