package core

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/engine/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/engine/errs"
)

// fakeMeta is a minimal in-memory metaStore for unit testing the
// descriptor-cache codec. It supports the two methods the
// cache uses (Get/SetMeta) and tracks call counts for tests
// that care about write ordering.
type fakeMeta struct {
	data   map[string]string
	writes int
}

func newFakeMeta() *fakeMeta {
	return &fakeMeta{data: make(map[string]string)}
}

func (m *fakeMeta) GetMeta(_ context.Context, key string) (string, error) {
	v, ok := m.data[key]
	if !ok {
		return "", errs.ErrMetaKeyNotFound
	}
	return v, nil
}

func (m *fakeMeta) SetMeta(_ context.Context, key, value string) error {
	m.data[key] = value
	m.writes++
	return nil
}

// validDescriptor returns a descriptor with one of every field
// set, matching what Persist would produce post-InitStore.
func validDescriptor(t *testing.T) *descriptor.Descriptor {
	t.Helper()
	return &descriptor.Descriptor{
		StoreID:       "11111111-2222-3333-4444-555555555555",
		SchemaVersion: descriptor.CurrentSchemaVersion,
		Sequence:      7,
	}
}

// --- save → load round-trip ---

func TestDescriptorCache_RoundTrip(t *testing.T) {
	ctx := t.Context()
	meta := newFakeMeta()
	src := validDescriptor(t)

	if err := saveDescriptorCache(ctx, meta, src); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadDescriptorCache(ctx, meta)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got == nil {
		t.Fatal("load returned nil cache, want round-trip")
	}

	if got.Sequence != src.Sequence {
		t.Errorf("Sequence: got %d, want %d", got.Sequence, src.Sequence)
	}
	if len(got.Checksum) != descriptor.ChecksumLen {
		t.Errorf("Checksum length: got %d, want %d", len(got.Checksum), descriptor.ChecksumLen)
	}

	expectedBlob, _ := descriptor.Marshal(src)
	if !bytes.Equal(got.Blob, expectedBlob) {
		t.Error("Blob round-trip mismatch")
	}

	expectedSum, _ := descriptor.Checksum(src)
	if !bytes.Equal(got.Checksum, expectedSum) {
		t.Error("Checksum round-trip mismatch")
	}
}

// --- empty cache is a normal "rebuild from Location" signal ---

func TestDescriptorCache_AbsentReturnsNilNoError(t *testing.T) {
	ctx := t.Context()
	meta := newFakeMeta()
	got, err := loadDescriptorCache(ctx, meta)
	if err != nil {
		t.Fatalf("expected nil error on empty cache, got %v", err)
	}
	if got != nil {
		t.Errorf("expected nil cache on empty meta, got %+v", got)
	}
}

// --- partial cache is corruption ---

func TestDescriptorCache_PartialIsCorruption(t *testing.T) {
	cases := []struct {
		name string
		set  func(m *fakeMeta)
	}{
		{
			"only_blob",
			func(m *fakeMeta) {
				m.data[metaKeyDescriptorBlob] = `{"store_id":"x"}`
			},
		},
		{
			"only_sequence",
			func(m *fakeMeta) {
				m.data[metaKeyDescriptorSequence] = "1"
			},
		},
		{
			"blob_and_sequence_no_checksum",
			func(m *fakeMeta) {
				m.data[metaKeyDescriptorBlob] = `{"store_id":"x"}`
				m.data[metaKeyDescriptorSequence] = "1"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			meta := newFakeMeta()
			tc.set(meta)
			_, err := loadDescriptorCache(ctx, meta)
			if err == nil {
				t.Fatal("expected error on partial cache")
			}
			if !strings.Contains(err.Error(), "missing") &&
				!strings.Contains(err.Error(), "partial") {
				t.Errorf("error should mention partial state: %v", err)
			}
		})
	}
}

// --- internal-consistency violations ---

func TestDescriptorCache_RejectsSequenceMismatch(t *testing.T) {
	ctx := t.Context()
	meta := newFakeMeta()
	src := validDescriptor(t) // Sequence = 7
	if err := saveDescriptorCache(ctx, meta, src); err != nil {
		t.Fatal(err)
	}
	// Hand-edit: bump the stored sequence so it disagrees with
	// what the blob encodes.
	meta.data[metaKeyDescriptorSequence] = "999"

	_, err := loadDescriptorCache(ctx, meta)
	if err == nil {
		t.Fatal("expected error on sequence mismatch")
	}
	if !strings.Contains(err.Error(), "sequence mismatch") {
		t.Errorf("error should mention sequence mismatch: %v", err)
	}
}

func TestDescriptorCache_RejectsChecksumMismatch(t *testing.T) {
	ctx := t.Context()
	meta := newFakeMeta()
	src := validDescriptor(t)
	if err := saveDescriptorCache(ctx, meta, src); err != nil {
		t.Fatal(err)
	}
	// Hand-edit: corrupt the checksum.
	bogus := make([]byte, descriptor.ChecksumLen)
	meta.data[metaKeyDescriptorChecksum] = hex.EncodeToString(bogus)

	_, err := loadDescriptorCache(ctx, meta)
	if err == nil {
		t.Fatal("expected error on checksum mismatch")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error should mention checksum mismatch: %v", err)
	}
}

// --- malformed encodings ---

func TestDescriptorCache_RejectsUnparseableSequence(t *testing.T) {
	ctx := t.Context()
	meta := newFakeMeta()
	meta.data[metaKeyDescriptorBlob] = `{"store_id":"x","schema_version":1,"sequence":1}`
	meta.data[metaKeyDescriptorSequence] = "not-a-number"
	meta.data[metaKeyDescriptorChecksum] = strings.Repeat("00", descriptor.ChecksumLen)
	_, err := loadDescriptorCache(ctx, meta)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestDescriptorCache_RejectsUnparseableChecksum(t *testing.T) {
	ctx := t.Context()
	meta := newFakeMeta()
	meta.data[metaKeyDescriptorBlob] = `{"store_id":"x","schema_version":1,"sequence":1}`
	meta.data[metaKeyDescriptorSequence] = "1"
	meta.data[metaKeyDescriptorChecksum] = "not hex bytes!"
	_, err := loadDescriptorCache(ctx, meta)
	if err == nil {
		t.Fatal("expected hex decode error")
	}
}

func TestDescriptorCache_RejectsBadChecksumLength(t *testing.T) {
	ctx := t.Context()
	meta := newFakeMeta()
	meta.data[metaKeyDescriptorBlob] = `{"store_id":"x","schema_version":1,"sequence":1}`
	meta.data[metaKeyDescriptorSequence] = "1"
	meta.data[metaKeyDescriptorChecksum] = "ab" // 1 byte, not 32
	_, err := loadDescriptorCache(ctx, meta)
	if err == nil {
		t.Fatal("expected length error")
	}
}

// --- save writes exactly three keys ---

func TestDescriptorCache_SaveWritesThreeKeys(t *testing.T) {
	ctx := t.Context()
	meta := newFakeMeta()
	src := validDescriptor(t)
	if err := saveDescriptorCache(ctx, meta, src); err != nil {
		t.Fatal(err)
	}
	if meta.writes != 3 {
		t.Errorf("writes: got %d, want 3", meta.writes)
	}
	for _, k := range []string{
		metaKeyDescriptorBlob,
		metaKeyDescriptorSequence,
		metaKeyDescriptorChecksum,
	} {
		if _, ok := meta.data[k]; !ok {
			t.Errorf("key %q not written", k)
		}
	}
}

// --- save → save overwrites ---

func TestDescriptorCache_SaveOverwrites(t *testing.T) {
	ctx := t.Context()
	meta := newFakeMeta()
	a := validDescriptor(t)
	a.Sequence = 5
	b := validDescriptor(t)
	b.Sequence = 12

	if err := saveDescriptorCache(ctx, meta, a); err != nil {
		t.Fatal(err)
	}
	if err := saveDescriptorCache(ctx, meta, b); err != nil {
		t.Fatal(err)
	}

	got, err := loadDescriptorCache(ctx, meta)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sequence != 12 {
		t.Errorf("Sequence: got %d, want 12 (overwrite)", got.Sequence)
	}
}

// --- error propagation from underlying meta ---

type errMeta struct{ err error }

func (m *errMeta) GetMeta(context.Context, string) (string, error) { return "", m.err }
func (m *errMeta) SetMeta(context.Context, string, string) error   { return m.err }

func TestDescriptorCache_LoadPropagatesIOError(t *testing.T) {
	ctx := t.Context()
	sentinel := errors.New("disk on fire")
	_, err := loadDescriptorCache(ctx, &errMeta{err: sentinel})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error should wrap underlying: got %v", err)
	}
}

func TestDescriptorCache_SavePropagatesIOError(t *testing.T) {
	ctx := t.Context()
	sentinel := errors.New("disk on fire")
	src := validDescriptor(t)
	err := saveDescriptorCache(ctx, &errMeta{err: sentinel}, src)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("error should wrap underlying: got %v", err)
	}
}
