package domain

import "errors"

// Domain-level sentinel errors. Conditions that any layer of the
// system can encounter when working with manifests, blobs, and
// configuration values — independent of Store lifecycle, leases,
// or encryption operations (those live in core/errors.go).

// --- Integrity ---

// ErrCorruptedManifest — the hash of the manifest file does not
// match its ArtifactID.
var ErrCorruptedManifest = errors.New("domain: corrupted manifest")

// ErrCorruptedBlob — the hash of the physical blob does not match
// its BlobRef.
var ErrCorruptedBlob = errors.New("domain: corrupted blob")

// ErrCorruptedContent — after the inverse Pipeline the hash does
// not match ContentHash.
var ErrCorruptedContent = errors.New("domain: corrupted content")

// --- Format and schema ---

// ErrUnsupportedSchemaVersion — the manifest's schema_version is
// not supported.
var ErrUnsupportedSchemaVersion = errors.New("domain: unsupported schema version")

// ErrUnknownPackFormat — the magic bytes of a .pack volume are
// unrecognised.
var ErrUnknownPackFormat = errors.New("domain: unknown pack format")

// --- Validation and limits ---

// ErrInvalidKDFParams — KDFParams fail the minimum-validity check:
// Time < 1, Memory < 19456 KiB, Threads < 1.
var ErrInvalidKDFParams = errors.New("domain: invalid KDF params")

// ErrInvalidTombstoneGracePeriod — TombstoneGracePeriod < 1h.
// A dedicated sentinel: this is the only parameter with runtime
// implications for multi-host safety.
var ErrInvalidTombstoneGracePeriod = errors.New("domain: invalid tombstone grace period")

// ErrNamespaceTooLong — namespace > 255 bytes.
var ErrNamespaceTooLong = errors.New("domain: namespace too long")

// ErrSessionIDTooLong — SessionID > 255 bytes.
var ErrSessionIDTooLong = errors.New("domain: session ID too long")

// ErrEmptySessionID — RollbackSession called with an empty string;
// guards against a mass deletion of sessionless artifacts.
var ErrEmptySessionID = errors.New("domain: empty session ID")

// ErrMetadataTooLarge — Artifact.Metadata > 64 KB.
var ErrMetadataTooLarge = errors.New("domain: metadata too large")

// ErrManifestTooLarge — serialised Manifest > 1 MB.
var ErrManifestTooLarge = errors.New("domain: manifest too large")

// ErrReservedNamespace — an attempt to use "*" or the "system."
// prefix without a CapabilityToken.
var ErrReservedNamespace = errors.New("domain: reserved namespace")

// --- Walk control ---

// ErrStopWalk — the callback for Walk/WalkSystem returns this
// sentinel for an early but successful exit.
var ErrStopWalk = errors.New("domain: stop walk")
