package bundler

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index/extension"
)

// fakeExtStore is an in-memory extension.ExtensionStore used to test
// the bundler index-extension in isolation, without a sqlite backend.
type fakeExtStore struct {
	data map[string][]byte // key: table + "\x00" + key
}

func newFakeExtStore() *fakeExtStore { return &fakeExtStore{data: map[string][]byte{}} }

var _ extension.ExtensionStore = (*fakeExtStore)(nil)

func (f *fakeExtStore) compositeKey(table, key string) string { return table + "\x00" + key }

func (f *fakeExtStore) Put(table, key string, value []byte) error {
	cp := make([]byte, len(value))
	copy(cp, value)
	f.data[f.compositeKey(table, key)] = cp
	return nil
}

func (f *fakeExtStore) Get(table, key string) ([]byte, bool, error) {
	v, ok := f.data[f.compositeKey(table, key)]
	return v, ok, nil
}

func (f *fakeExtStore) Delete(table, key string) error {
	delete(f.data, f.compositeKey(table, key))
	return nil
}

func (f *fakeExtStore) DeletePrefix(table, prefix string) error {
	if prefix == "" {
		return extension.ErrEmptyPrefix
	}
	p := f.compositeKey(table, prefix)
	for k := range f.data {
		if strings.HasPrefix(k, p) {
			delete(f.data, k)
		}
	}
	return nil
}

func (f *fakeExtStore) Scan(table, prefix string, cb func(key string, value []byte) error) error {
	tablePrefix := table + "\x00"
	keyPrefix := f.compositeKey(table, prefix)
	for k, v := range f.data {
		if !strings.HasPrefix(k, tablePrefix) {
			continue
		}
		if prefix != "" && !strings.HasPrefix(k, keyPrefix) {
			continue
		}
		if err := cb(strings.TrimPrefix(k, tablePrefix), v); err != nil {
			if errors.Is(err, extension.ErrStopScan) {
				return nil
			}
			return err
		}
	}
	return nil
}

func (f *fakeExtStore) Inc(table, key string, delta int64) (int64, error) {
	var cur int64
	if v, ok := f.data[f.compositeKey(table, key)]; ok && len(v) == 8 {
		cur = int64(binary.BigEndian.Uint64(v))
	}
	cur += delta
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, uint64(cur))
	f.data[f.compositeKey(table, key)] = b
	return cur, nil
}

// newTestExtension returns a bundler index-extension wired to a fresh
// in-memory store (Setup already run, db-mode equivalent).
func newTestExtension(t *testing.T) (*indexExtension, *fakeExtStore) {
	t.Helper()
	e := &indexExtension{}
	store := newFakeExtStore()
	if err := e.Setup(context.Background(), store, 0); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	return e, store
}

func TestIndexExtension_RecordResolveDelete(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestExtension(t)

	container := domain.Manifest{BlobRef: "pack-blob-1"}
	entries := []PackedEntry{
		{
			ArtifactID:     "art-p1",
			BlobRef:        "blob-p1",
			ManifestOffset: 0,
			ManifestSize:   200,
			BlobOffset:     200,
			BlobSize:       1024,
			PipelineParams: []byte("pp1"),
		},
		{
			ArtifactID:     "art-p2",
			BlobRef:        "blob-p2",
			ManifestOffset: 1224,
			ManifestSize:   200,
			BlobOffset:     1424,
			BlobSize:       2048,
		},
	}
	if err := e.RecordPack(ctx, container, entries); err != nil {
		t.Fatalf("RecordPack: %v", err)
	}

	// Hit: placement reflects the container's body blob and the slice.
	ov, ok, err := e.ResolvePacked(ctx, "art-p1")
	if err != nil {
		t.Fatalf("ResolvePacked(art-p1): %v", err)
	}
	if !ok {
		t.Fatal("art-p1: expected found")
	}
	if ov.PackBlobRef != "pack-blob-1" {
		t.Errorf("PackBlobRef: got %q, want pack-blob-1", ov.PackBlobRef)
	}
	if ov.ManifestOffset != 0 || ov.ManifestSize != 200 {
		t.Errorf("manifest slice: got off=%d size=%d, want 0/200", ov.ManifestOffset, ov.ManifestSize)
	}
	if ov.BlobOffset != 200 || ov.BlobSize != 1024 {
		t.Errorf("blob slice: got off=%d size=%d, want 200/1024", ov.BlobOffset, ov.BlobSize)
	}
	if string(ov.PipelineParams) != "pp1" {
		t.Errorf("PipelineParams: got %q, want pp1", ov.PipelineParams)
	}

	// Miss: an unpacked artifact is not owned here.
	if _, ok, err := e.ResolvePacked(ctx, "not-packed"); err != nil || ok {
		t.Errorf("ResolvePacked(not-packed): got ok=%v err=%v, want false/nil", ok, err)
	}

	// DeletePack drops every member of the volume.
	if err := e.DeletePack(ctx, "pack-blob-1"); err != nil {
		t.Fatalf("DeletePack: %v", err)
	}
	for _, id := range []domain.ArtifactID{"art-p1", "art-p2"} {
		if _, ok, _ := e.ResolvePacked(ctx, id); ok {
			t.Errorf("%s: still resolvable after DeletePack", id)
		}
	}
}

func TestIndexExtension_DeletePackIsVolumeScoped(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestExtension(t)

	if err := e.RecordPack(ctx, domain.Manifest{BlobRef: "vol-A"},
		[]PackedEntry{{ArtifactID: "a1", BlobSize: 1}}); err != nil {
		t.Fatalf("RecordPack vol-A: %v", err)
	}
	if err := e.RecordPack(ctx, domain.Manifest{BlobRef: "vol-B"},
		[]PackedEntry{{ArtifactID: "b1", BlobSize: 1}}); err != nil {
		t.Fatalf("RecordPack vol-B: %v", err)
	}

	if err := e.DeletePack(ctx, "vol-A"); err != nil {
		t.Fatalf("DeletePack vol-A: %v", err)
	}
	if _, ok, _ := e.ResolvePacked(ctx, "a1"); ok {
		t.Error("a1: should be gone after DeletePack(vol-A)")
	}
	if _, ok, _ := e.ResolvePacked(ctx, "b1"); !ok {
		t.Error("b1: should survive DeletePack(vol-A)")
	}
}

// TestIndexExtension_SatisfiesResolver pins the capability contract:
// the value returned by the constructor must be assertable to
// extension.Resolver (the core overlay probes by assertion, ADR-84).
func TestIndexExtension_SatisfiesResolver(t *testing.T) {
	var ext extension.IndexExtension = NewIndexExtension()
	if _, ok := ext.(extension.Resolver); !ok {
		t.Fatal("bundler index-extension does not satisfy extension.Resolver")
	}
}

// TestIndexExtension_RecordBeforeSetup guards the store-capture
// precondition.
func TestIndexExtension_RecordBeforeSetup(t *testing.T) {
	e := &indexExtension{}
	if err := e.RecordPack(context.Background(), domain.Manifest{BlobRef: "x"}, nil); err == nil {
		t.Fatal("RecordPack before Setup: expected error")
	}
}
