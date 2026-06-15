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
// it externally. Returns a wrapped error from engine/artifact when
// exceeded.
const MaxKeyIDLength = 255

// MaxExtSize is the maximum byte size of the Manifest Ext
// block (custom index data the engine itself reads — fsmeta, etc).
// 64 KiB. Returns errs.ErrExtTooLarge when exceeded.
const MaxExtSize = 64 * 1024

// MaxUsrSize is the maximum byte size of the Manifest Usr block
// (opaque host-application data). 64 KiB. Returns
// errs.ErrUsrTooLarge when exceeded.
const MaxUsrSize = 64 * 1024

// MaxBlobRefs and MaxHandleRefs cap the manifest reference arrays
// (ADR-92/93): the on-disk chunk/member list is 16-bit length-counted, so
// each array holds at most 65535 entries. Exceeding either returns
// errs.ErrTooManyRefs on write. The encode path has no overall byte cap —
// it is bounded field-by-field — while reads are guarded by
// MaxManifestSize (below) against corrupt or oversized files.
const (
	MaxBlobRefs   = 65535
	MaxHandleRefs = 65535
)

// MaxManifestSize bounds a manifest file on READ, so a corrupt or hostile
// file cannot force an unbounded allocation before the per-field limits
// (checked post-parse) can fire. It is derived from the per-field maxima,
// not arbitrary: a worst-case well-formed manifest is
// (MaxBlobRefs + MaxHandleRefs) hex digests + MaxExtSize + MaxUsrSize +
// fixed overhead — ~9 MiB for SHA-256, ~17 MiB for SHA-512. 32 MiB leaves
// headroom for the longest registered hash. The encode path does NOT use
// this — it is bounded field-by-field. Returns errs.ErrManifestTooLarge.
const MaxManifestSize = 32 * 1024 * 1024

// SegmentSize bounds for the segmented AEAD blob format (ADR-59,
// docs/4 §11.1 "SegmentSize"). The on-disk header stores the
// segment size as a uint32, and a too-small segment makes the
// per-segment overhead (12-byte IV + 16-byte tag + 4-byte length)
// dominate, so we constrain it to a sane window. An out-of-range
// value at InitStore returns errs.ErrInvalidConfig.
const (
	// DefaultSegmentSize is the plaintext segment size chosen for an
	// encrypting store that leaves StoreConfig.SegmentSize at zero.
	// ≈1 MiB — format overhead ~0.003%.
	DefaultSegmentSize = 1 << 20
	// MinSegmentSize is the smallest permitted segment size (4 KiB).
	MinSegmentSize = 4 * 1024
	// MaxSegmentSize is the largest permitted segment size (1 GiB),
	// comfortably within the uint32 header field.
	MaxSegmentSize = 1 << 30
)

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
