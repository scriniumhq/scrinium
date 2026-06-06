package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/errs"
)

// MarkVerified, DeletePacked, and MarkVerified-related listing
// behaviour live in the conformance suite at
// internal/testutil/indextest. This file is for sqlite-specific
// behaviour: WriteCheckpoint (the optional index.CheckpointWriter
// capability, which sqlite implements via VACUUM INTO — it needs an
// on-disk source and so does not map to in-memory backends, and other
// backends such as Postgres need not implement it at all), and the
// store_meta storage details that rely on SQLite's UPSERT and TEXT
// encoding.

// --- WriteCheckpoint ---

func TestWriteCheckpoint_CreatesCheckpoint(t *testing.T) {
	idx, _ := newDiskIndex(t)
	// Seed some data so the checkpoint is a meaningful copy.
	insertBlob(t, idx, "blob-1", "sha256-"+strings.Repeat("a", 64), 1024,
		domain.PhysicalAddress{Path: "p"}, 1)
	insertManifest(t, idx, domain.Manifest{
		ArtifactID: "art-1", Type: domain.ManifestTypeBlob, Namespace: "ns",
		BlobRef: "blob-1", CreatedAt: time.Now(),
	})

	dest := filepath.Join(t.TempDir(), "snap.db")
	if err := idx.WriteCheckpoint(context.Background(), dest); err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}

	// File exists.
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat checkpoint: %v", err)
	}
	if st.Size() == 0 {
		t.Error("checkpoint file is empty")
	}

	// Checkpoint is a self-contained, openable database with the
	// same data.
	snap, err := NewStore(context.Background(), dest)
	if err != nil {
		t.Fatalf("NewStore checkpoint: %v", err)
	}

	if got := countRows(t, snap, "blobs"); got != 1 {
		t.Errorf("checkpoint blobs: got %d, want 1", got)
	}
	if got := countRows(t, snap, "manifests"); got != 1 {
		t.Errorf("checkpoint manifests: got %d, want 1", got)
	}
}

func TestWriteCheckpoint_RejectsExistingFile(t *testing.T) {
	idx, _ := newDiskIndex(t)
	dest := filepath.Join(t.TempDir(), "exists.db")
	if err := os.WriteFile(dest, []byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := idx.WriteCheckpoint(context.Background(), dest)
	if err == nil {
		t.Fatal("expected error on existing destination")
	}
}

func TestWriteCheckpoint_RejectsMemoryDest(t *testing.T) {
	idx := newMemoryIndex(t)
	err := idx.WriteCheckpoint(context.Background(), ":memory:")
	if err == nil {
		t.Fatal("expected error on :memory: destination")
	}
}

func TestWriteCheckpoint_RejectsEmptyPath(t *testing.T) {
	idx := newMemoryIndex(t)
	err := idx.WriteCheckpoint(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on empty path")
	}
}

func TestWriteCheckpoint_CreatesParentDir(t *testing.T) {
	idx, _ := newDiskIndex(t)
	dest := filepath.Join(t.TempDir(), "deep", "nested", "snap.db")
	if err := idx.WriteCheckpoint(context.Background(), dest); err != nil {
		t.Fatalf("WriteCheckpoint with nested parent: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("checkpoint not created: %v", err)
	}
}

// --- GetMeta / SetMeta ---

func TestSetMeta_GetMeta_RoundTrip(t *testing.T) {
	ctx := t.Context()
	idx := newMemoryIndex(t)
	if err := idx.SetMeta(ctx, "schema_notes", "v1: initial"); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	got, err := idx.GetMeta(ctx, "schema_notes")
	if err != nil {
		t.Fatalf("GetMeta: %v", err)
	}
	if got != "v1: initial" {
		t.Errorf("value: got %q, want %q", got, "v1: initial")
	}
}

func TestSetMeta_Overwrites(t *testing.T) {
	ctx := t.Context()
	idx := newMemoryIndex(t)
	if err := idx.SetMeta(ctx, "k", "first"); err != nil {
		t.Fatal(err)
	}
	if err := idx.SetMeta(ctx, "k", "second"); err != nil {
		t.Fatal(err)
	}
	got, _ := idx.GetMeta(ctx, "k")
	if got != "second" {
		t.Errorf("got %q, want %q", got, "second")
	}
	// Still one row total — the UPSERT replaced, not appended.
	if got := countRows(t, idx, "store_meta"); got != 1 {
		t.Errorf("store_meta rows: got %d, want 1", got)
	}
}

func TestGetMeta_Missing(t *testing.T) {
	ctx := t.Context()
	idx := newMemoryIndex(t)
	_, err := idx.GetMeta(ctx, "never-set")
	if !errors.Is(err, errs.ErrMetaKeyNotFound) {
		t.Fatalf("expected errs.ErrMetaKeyNotFound, got %v", err)
	}
}

func TestSetMeta_BinarySafe(t *testing.T) {
	ctx := t.Context()
	idx := newMemoryIndex(t)
	// Unicode, tabs, newlines, quotes — store_meta must survive
	// arbitrary text payloads.
	weird := "lineA\nlineB\tcol\u200b\"quoted'mixed"
	if err := idx.SetMeta(ctx, "weird", weird); err != nil {
		t.Fatal(err)
	}
	got, err := idx.GetMeta(ctx, "weird")
	if err != nil {
		t.Fatal(err)
	}
	if got != weird {
		t.Errorf("value mangled: got %q, want %q", got, weird)
	}
}

// --- Compile-time interface conformance ---

func TestIndex_ImplementsStoreIndex(t *testing.T) {
	// The compile-time check var _ store.StoreIndex = (*Index)(nil)
	// in sqlite.go is the real guarantee; this test just confirms
	// it at runtime so a regression shows up in test output, not
	// just a build error.
	var _ index.StoreIndex = (*Index)(nil)
	// WriteCheckpoint moved off the mandatory StoreIndex into the optional
	// CheckpointWriter capability; sqlite implements it.
	var _ index.CheckpointWriter = (*Index)(nil)
	idx := newMemoryIndex(t)
	var asInterface index.StoreIndex = idx
	if asInterface == nil {
		t.Fatal("Index does not satisfy store.StoreIndex")
	}
}
