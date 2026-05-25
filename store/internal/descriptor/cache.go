package descriptor

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"scrinium.dev/errs"
)

// store_meta keys for the descriptor L2 cache.
const (
	metaKeyDescriptorBlob     = "descriptor_blob"
	metaKeyDescriptorSequence = "descriptor_sequence"
	metaKeyDescriptorChecksum = "descriptor_checksum"
)

// MetaStore is the narrow store_meta surface the cache needs. The
// engine's StoreIndex satisfies it implicitly.
type MetaStore interface {
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

// Cache is the L2 cached projection of the on-disk descriptor: a
// fast-start aid, never authoritative. A missing or corrupt cache is
// always recoverable from the L0/L1 replicas. The three fields are
// written and read as a unit; partial state surfaces as an error.
type Cache struct {
	Blob     []byte // full JSON serialisation, byte-identical to L0
	Sequence uint64 // duplicates Blob's sequence, so staleness is checkable without parsing
	Checksum []byte // SHA-256 over Blob, ChecksumLen bytes
}

// Load reads the three cache keys out of meta. Returns (nil, nil)
// when the cache is fully absent (fresh host); (nil, err) when it is
// partial or internally inconsistent (caller rebuilds from Location);
// (cache, nil) when present and consistent.
func Load(ctx context.Context, meta MetaStore) (*Cache, error) {
	blob, blobErr := meta.GetMeta(ctx, metaKeyDescriptorBlob)
	seqStr, seqErr := meta.GetMeta(ctx, metaKeyDescriptorSequence)
	csumHex, csumErr := meta.GetMeta(ctx, metaKeyDescriptorChecksum)

	missing := 0
	for _, err := range []error{blobErr, seqErr, csumErr} {
		if errors.Is(err, errs.ErrMetaKeyNotFound) {
			missing++
		}
	}
	switch missing {
	case 3:
		return nil, nil
	case 0:
		// fall through to validation
	default:
		return nil, fmt.Errorf("descriptor cache: %d/3 keys missing — partial state", missing)
	}

	for _, err := range []error{blobErr, seqErr, csumErr} {
		if err != nil && !errors.Is(err, errs.ErrMetaKeyNotFound) {
			return nil, fmt.Errorf("descriptor cache: read: %w", err)
		}
	}

	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("descriptor cache: parse sequence: %w", err)
	}
	csum, err := hex.DecodeString(csumHex)
	if err != nil {
		return nil, fmt.Errorf("descriptor cache: parse checksum: %w", err)
	}
	if len(csum) != ChecksumLen {
		return nil, fmt.Errorf("descriptor cache: checksum length %d, want %d", len(csum), ChecksumLen)
	}

	cache := &Cache{Blob: []byte(blob), Sequence: seq, Checksum: csum}
	if err := validateConsistency(cache); err != nil {
		return nil, fmt.Errorf("descriptor cache: %w", err)
	}
	return cache, nil
}

// Save writes the cache for d to meta. The three keys are written
// sequentially; a crash mid-trio leaves a partial cache that the next
// Load rejects, prompting a re-save. Idempotent.
func Save(ctx context.Context, meta MetaStore, d *Descriptor) error {
	blob, err := Marshal(d)
	if err != nil {
		return fmt.Errorf("descriptor cache: marshal: %w", err)
	}
	csum, err := Checksum(d)
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

// Refresh rewrites the cache when it is absent, corrupt, or diverged
// from canonical. A load error is swallowed on purpose: the cache is
// recoverable from Location, and Save is idempotent.
func Refresh(ctx context.Context, meta MetaStore, canonical *Descriptor) error {
	cache, _ := Load(ctx, meta)
	if cache != nil {
		want, err := Checksum(canonical)
		if err != nil {
			return fmt.Errorf("checksum canonical: %w", err)
		}
		if bytes.Equal(cache.Checksum, want) {
			return nil
		}
	}
	if err := Save(ctx, meta, canonical); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	return nil
}

// validateConsistency verifies the stored sequence and checksum agree
// with what Blob itself encodes, rejecting hand-edited or
// partially-written cache state.
func validateConsistency(c *Cache) error {
	d, err := Unmarshal(c.Blob)
	if err != nil {
		return fmt.Errorf("blob does not parse: %w", err)
	}
	if d.Sequence != c.Sequence {
		return fmt.Errorf("sequence mismatch: blob says %d, cache says %d", d.Sequence, c.Sequence)
	}
	expected, err := Checksum(d)
	if err != nil {
		return fmt.Errorf("recompute checksum: %w", err)
	}
	if !bytes.Equal(expected, c.Checksum) {
		return errors.New("checksum mismatch between blob and stored checksum")
	}
	return nil
}
