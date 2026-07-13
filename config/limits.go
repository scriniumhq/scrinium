package config

import "time"

// Configurable limits — the numeric bounds on StoreConfig fields. Every
// constant here is OVERRIDABLE: it constrains a field an operator sets
// in StoreConfig, and the field registry validates the input against it
// (registry.go). To widen or tighten what a store will accept, this is
// the file to edit.
//
// Fixed on-disk format/protocol caps (identifier lengths, manifest block
// sizes, reference counts) are NOT configurable and live with the
// manifest in domain (manifest_limits.go); the segmented-AEAD format
// keeps its own OOM read-guard bounds in
// engine/pipeline/internal/segaead, separate from these
// input-validation bounds.

// SegmentSize bounds for the segmented AEAD blob format (ADR-59). The
// on-disk header stores the segment size as a uint32, and a too-small
// segment makes the per-segment overhead (12-byte IV + 16-byte tag +
// 4-byte length) dominate, so the value is constrained to a sane
// window. An out-of-range value at InitStore returns errs.ErrInvalidConfig.
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

// MinTombstoneGracePeriod is the minimum non-zero TombstoneGracePeriod.
// 1 hour. Returns errs.ErrInvalidTombstoneGracePeriod when violated. A
// shorter period breaks the Revive flow across hosts.
const MinTombstoneGracePeriod = time.Hour

// MaxInlineBlobLimit is the maximum value StoreConfig.InlineBlobLimit
// can take. 64 KiB. Bigger limits would push hot index pages out of the
// SQLite page cache because inline blobs live inside the manifest row.
// Returns errs.ErrInvalidConfig when exceeded.
const MaxInlineBlobLimit = 64 * 1024

// MinRetentionPeriod is the minimum non-zero RetentionPeriod. 1 hour. A
// shorter period makes deferred deletion pointless — by the time GC
// runs, the retention window has already expired. Returns
// errs.ErrInvalidConfig when violated.
const MinRetentionPeriod = time.Hour
