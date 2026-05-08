package core

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/rkurbatov/scrinium/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/errs"
)

// store_meta keys for the descriptor L2 cache, per §10.1.5. The
// cache is a fast-start aid: with it OpenStore can verify that
// the on-disk descriptor matches what the previous session saw,
// without re-parsing it. Without it OpenStore simply re-reads
// L0/L1 — the cache is never authoritative.
const (
	metaKeyDescriptorBlob     = "descriptor_blob"
	metaKeyDescriptorSequence = "descriptor_sequence"
	metaKeyDescriptorChecksum = "descriptor_checksum"
)

// DescriptorCache is the L2 cached projection of the on-disk
// descriptor. The three fields are written together by
// saveDescriptorCache; corruption (partial write, manual edit)
// surfaces at load time as an error rather than a half-populated
// struct.
type DescriptorCache struct {
	// Blob is the full JSON serialisation of the descriptor —
	// byte-identical to what Persist would write to L0.
	Blob []byte

	// Sequence duplicates Blob's sequence field. Held separately
	// so that "is the cache stale relative to Location" can be
	// answered without parsing Blob.
	Sequence uint64

	// Checksum is SHA-256 over Blob. ChecksumLen bytes; hex-
	// encoded in store_meta.
	Checksum []byte
}

// loadDescriptorCache reads the three cache keys out of meta.
//
// Outcomes:
//   - (cache, nil) — all three keys present and internally
//     consistent (checksum matches blob, sequence matches blob).
//   - (nil, nil)   — the cache does not exist. This is the
//     fresh-host or first-OpenStore-after-corrupt-meta case;
//     OpenStore treats it as "rebuild from Location".
//   - (nil, err)   — the cache exists but is malformed. Either
//     a partial write (some keys present, some not) or
//     content-mismatch (checksum/sequence does not agree with
//     blob). The caller MUST NOT use the returned data; the
//     correct recovery is to re-derive the cache from the
//     authoritative Location replicas.
func loadDescriptorCache(ctx context.Context, meta metaStore) (*DescriptorCache, error) {
	blob, blobErr := meta.GetMeta(ctx, metaKeyDescriptorBlob)
	seqStr, seqErr := meta.GetMeta(ctx, metaKeyDescriptorSequence)
	csumHex, csumErr := meta.GetMeta(ctx, metaKeyDescriptorChecksum)

	missing := 0
	if errors.Is(blobErr, errs.ErrMetaKeyNotFound) {
		missing++
	}
	if errors.Is(seqErr, errs.ErrMetaKeyNotFound) {
		missing++
	}
	if errors.Is(csumErr, errs.ErrMetaKeyNotFound) {
		missing++
	}
	switch missing {
	case 3:
		// Fully empty cache — normal fresh-host condition.
		return nil, nil
	case 0:
		// All present — fall through to validation below.
	default:
		// Partial — treat as corruption. Caller will rebuild.
		return nil, fmt.Errorf("descriptor cache: %d/3 keys missing — partial state", missing)
	}

	// Surface any non-NotFound errors (I/O, classifier-level
	// failures from the index).
	for _, err := range []error{blobErr, seqErr, csumErr} {
		if err != nil && !errors.Is(err, errs.ErrMetaKeyNotFound) {
			return nil, fmt.Errorf("descriptor cache: read: %w", err)
		}
	}

	// Parse sequence.
	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("descriptor cache: parse sequence: %w", err)
	}

	// Parse checksum.
	csum, err := hex.DecodeString(csumHex)
	if err != nil {
		return nil, fmt.Errorf("descriptor cache: parse checksum: %w", err)
	}
	if len(csum) != descriptor.ChecksumLen {
		return nil, fmt.Errorf("descriptor cache: checksum length %d, want %d",
			len(csum), descriptor.ChecksumLen)
	}

	cache := &DescriptorCache{
		Blob:     []byte(blob),
		Sequence: seq,
		Checksum: csum,
	}

	// Internal consistency: the sequence and checksum stored
	// alongside the blob must match what the blob itself encodes.
	// A mismatch means the cache was hand-edited or written by
	// a buggy version; refuse to use it.
	if err := validateCacheConsistency(cache); err != nil {
		return nil, fmt.Errorf("descriptor cache: %w", err)
	}

	return cache, nil
}

// saveDescriptorCache writes the cache for d to meta. The three
// keys are written sequentially; SetMeta is atomic per key, but
// the trio is not atomic across a crash. A crash mid-trio leaves
// a partial cache, which the next loadDescriptorCache rejects as
// corruption — the caller then re-saves. The flow is idempotent.
func saveDescriptorCache(ctx context.Context, meta metaStore, d *descriptor.Descriptor) error {
	blob, err := descriptor.Marshal(d)
	if err != nil {
		return fmt.Errorf("descriptor cache: marshal: %w", err)
	}
	csum, err := descriptor.Checksum(d)
	if err != nil {
		return fmt.Errorf("descriptor cache: checksum: %w", err)
	}

	if err := meta.SetMeta(ctx, metaKeyDescriptorBlob, string(blob)); err != nil {
		return fmt.Errorf("descriptor cache: write blob: %w", err)
	}
	if err := meta.SetMeta(ctx, metaKeyDescriptorSequence, strconv.FormatUint(d.Sequence, 10)); err != nil {
		return fmt.Errorf("descriptor cache: write sequence: %w", err)
	}
	if err := meta.SetMeta(ctx, metaKeyDescriptorChecksum, hex.EncodeToString(csum)); err != nil {
		return fmt.Errorf("descriptor cache: write checksum: %w", err)
	}
	return nil
}

// validateCacheConsistency verifies that the sequence and
// checksum stored alongside the blob agree with what the blob
// itself encodes. Used by loadDescriptorCache to reject
// hand-edited or partially-written cache state.
func validateCacheConsistency(c *DescriptorCache) error {
	d, err := descriptor.Unmarshal(c.Blob)
	if err != nil {
		return fmt.Errorf("blob does not parse: %w", err)
	}
	if d.Sequence != c.Sequence {
		return fmt.Errorf("sequence mismatch: blob says %d, cache says %d",
			d.Sequence, c.Sequence)
	}
	expected, err := descriptor.Checksum(d)
	if err != nil {
		return fmt.Errorf("recompute checksum: %w", err)
	}
	if !bytes.Equal(expected, c.Checksum) {
		return errors.New("checksum mismatch between blob and stored checksum")
	}
	return nil
}
