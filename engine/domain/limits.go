package domain

import "time"

// System-wide hard limits enforced at input validation. Values
// come from the engine specification §5.6 "System limits"; a
// violation returns the sentinel listed below without performing
// the operation.
//
// Keep this file in sync with the spec table. Constants are
// added here only after the corresponding check is wired up;
// the TOC chunk count limit is reserved and will land with the
// chunker.Wrapper milestone (M5).

// MaxNamespaceLen is the maximum byte length of Namespace.
// Returns errs.ErrNamespaceTooLong when exceeded.
// Standard FS/DB name limit.
const MaxNamespaceLen = 255

// MaxSessionIDLen is the maximum byte length of SessionID.
// Returns errs.ErrSessionIDTooLong when exceeded.
// Standard FS/DB name limit.
const MaxSessionIDLen = 255

// MaxKeyIDLength is the upper bound on KeyID byte length in the
// manifest file header per §7.1. The KeyID-length byte is one
// octet, so the KeyID itself can be at most 255 bytes; producers
// that hit the limit must shorten the identifier or prefix-hash
// it externally. Returns a wrapped error from manifestcodec when
// exceeded.
const MaxKeyIDLength = 255

// MaxExtSize is the maximum byte size of the Manifest Ext
// block (extension data the engine itself reads — fsmeta, etc).
// 64 KiB. Returns errs.ErrExtTooLarge when exceeded.
const MaxExtSize = 64 * 1024

// MaxUsrSize is the maximum byte size of the Manifest Usr block
// (opaque host-application data). 64 KiB. Returns
// errs.ErrUsrTooLarge when exceeded.
const MaxUsrSize = 64 * 1024

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

// MaxInlineBlobLimit is the maximum value StoreConfig.InlineBlobLimit
// can take per docs/4 §5.6. 64 KiB. Bigger limits would push hot
// index pages out of SQLite page cache because inline blobs live
// inside the manifest row.
// Returns errs.ErrInvalidConfig when exceeded.
const MaxInlineBlobLimit = 64 * 1024

// MinRetentionPeriod is the minimum non-zero RetentionPeriod per
// docs/4 §5.6. 1 hour. A shorter period makes deferred deletion
// pointless — by the time GC runs, the retention window has
// already expired.
// Returns errs.ErrInvalidConfig when violated.
const MinRetentionPeriod = time.Hour
