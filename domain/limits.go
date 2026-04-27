package domain

import "time"

// System-wide hard limits enforced at input validation. Values
// come from the engine specification §5.6 "System limits"; a
// violation returns the sentinel listed below without performing
// the operation.
//
// Keep this file in sync with the spec table. Constants are
// added here only after the corresponding check is wired up;
// reserved limits (KeyID, InlineBlobLimit, RetentionPeriod,
// TOC chunk count) will land alongside their enforcement in
// later milestones.

// MaxNamespaceLen is the maximum byte length of Namespace.
// Returns errs.ErrNamespaceTooLong when exceeded.
// Standard FS/DB name limit.
const MaxNamespaceLen = 255

// MaxSessionIDLen is the maximum byte length of SessionID.
// Returns errs.ErrSessionIDTooLong when exceeded.
// Standard FS/DB name limit.
const MaxSessionIDLen = 255

// MaxMetadataSize is the maximum byte size of the Manifest
// Metadata block. 64 KiB.
// Returns errs.ErrMetadataTooLarge when exceeded.
// Metadata is for tags and paths, not for documents.
const MaxMetadataSize = 64 * 1024

// MaxManifestSize is the maximum byte size of a serialised
// Manifest. 1 MiB.
// Returns errs.ErrManifestTooLarge when exceeded.
// A manifest with thousands of chunks or huge Metadata is
// a design error.
const MaxManifestSize = 1024 * 1024

// MinTombstoneGracePeriod is the minimum non-zero
// TombstoneGracePeriod. 1 hour.
// Returns errs.ErrInvalidTombstoneGracePeriod when violated.
// A shorter period breaks the Revive flow across hosts.
const MinTombstoneGracePeriod = time.Hour
