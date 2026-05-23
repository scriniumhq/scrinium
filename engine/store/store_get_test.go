package store_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/internal/testutil/storefx"
)

// --- Round-trip: Put → Get → ReadAll ---

func TestGet_TargetRoundTrip(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	const text = "round-trip target"

	id, err := s.Put(context.Background(), payload(text), domain.PutOptions{Namespace: "rt"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != text {
		t.Errorf("payload: got %q, want %q", got, text)
	}
}

func TestGet_InlineRoundTrip(t *testing.T) {
	s, _ := newInlineStore(t, 100)
	const text = "round-trip inline"

	id, err := s.Put(context.Background(), payload(text), domain.PutOptions{Namespace: "rt"})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != text {
		t.Errorf("payload: got %q, want %q", got, text)
	}
}

// --- Empty payload, both layouts ---

func TestGet_EmptyTarget(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(nil)},
		domain.PutOptions{Namespace: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d bytes, want 0", len(got))
	}
}

func TestGet_EmptyInline(t *testing.T) {
	s, _ := newInlineStore(t, 100)
	id, err := s.Put(context.Background(),
		domain.Artifact{Payload: bytes.NewReader(nil)},
		domain.PutOptions{Namespace: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d bytes, want 0", len(got))
	}
}

// --- Manifest exposed before first Read ---

func TestGet_ManifestAvailableBeforeRead(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(),
		payload("manifest first"),
		domain.PutOptions{Namespace: "ns", SessionID: "sess-x"})
	if err != nil {
		t.Fatal(err)
	}
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	m := rh.Manifest()
	if m.ArtifactID != id {
		t.Errorf("ArtifactID: got %q, want %q", m.ArtifactID, id)
	}
	if m.Namespace != "ns" {
		t.Errorf("Namespace: got %q, want %q", m.Namespace, "ns")
	}
	if m.SessionID != "sess-x" {
		t.Errorf("SessionID: got %q, want %q", m.SessionID, "sess-x")
	}
	if m.LayoutHeader.BlobStorage != domain.LayoutTarget {
		t.Errorf("LayoutHeader: got %q, want Target", m.LayoutHeader.BlobStorage)
	}
}

// --- ReadAt mid-stream ---

func TestGet_ReadAt_TargetMidStream(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	const text = "abcdefghijklmnop" // 16 bytes
	id, err := s.Put(context.Background(), payload(text), domain.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	if !rh.SupportsRandomAccess() {
		t.Fatal("Target blob expected to support random access")
	}

	buf := make([]byte, 4)
	n, err := rh.ReadAt(buf, 5)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != 4 || string(buf) != "fghi" {
		t.Errorf("got n=%d buf=%q, want n=4 buf=\"fghi\"", n, buf)
	}
}

func TestGet_ReadAt_InlineMidStream(t *testing.T) {
	s, _ := newInlineStore(t, 100)
	const text = "abcdefghij"
	id, err := s.Put(context.Background(), payload(text), domain.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()

	buf := make([]byte, 3)
	n, err := rh.ReadAt(buf, 4)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != 3 || string(buf) != "efg" {
		t.Errorf("got n=%d buf=%q, want n=3 buf=\"efg\"", n, buf)
	}
}

// --- errs.ErrArtifactNotFound ---

func TestGet_NotFound(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	_, err := s.Get(context.Background(),
		domain.ArtifactID("sha256-"+strings.Repeat("0", 64)),
		domain.GetOptions{})
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("expected errs.ErrArtifactNotFound, got %v", err)
	}
}

func TestGet_EmptyID(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	_, err := s.Get(context.Background(), "", domain.GetOptions{})
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("expected errs.ErrArtifactNotFound, got %v", err)
	}
}

// --- errs.ErrCorruptedManifest via on-disk tampering ---

func TestGet_CorruptedManifest(t *testing.T) {
	s, root := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("tamper me"), domain.PutOptions{Namespace: "t"})
	if err != nil {
		t.Fatal(err)
	}

	// Locate and flip a byte inside the manifest body (past the
	// 5-byte header). Past the body starts at offset 5.
	path := storefx.OnDiskAt(root).ManifestPath(id)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if len(raw) < 10 {
		t.Fatalf("manifest unexpectedly short: %d bytes", len(raw))
	}
	raw[len(raw)-2] ^= 0x01
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	_, err = s.Get(context.Background(), id, domain.GetOptions{})
	if !errors.Is(err, errs.ErrCorruptedManifest) {
		t.Fatalf("expected errs.ErrCorruptedManifest, got %v", err)
	}
}

// --- errs.ErrCorruptedBlob: manifest exists but blob file is gone ---

func TestGet_CorruptedBlob(t *testing.T) {
	s, root := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("blob will vanish"), domain.PutOptions{Namespace: "v"})
	if err != nil {
		t.Fatal(err)
	}

	// Wipe every regular file under blobs/. The Get call itself
	// only reads the manifest, so it should still succeed; the
	// failure surfaces on the first Read.
	for _, p := range storefx.OnDiskAt(root).BlobFiles() {
		_ = os.Remove(p)
	}

	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get with missing blob should succeed (manifest still on disk): %v", err)
	}
	defer rh.Close()

	_, err = io.ReadAll(rh)
	if !errors.Is(err, errs.ErrCorruptedBlob) {
		t.Fatalf("Read with missing blob: expected errs.ErrCorruptedBlob, got %v", err)
	}
}

// --- Close idempotency ---

func TestGet_DoubleCloseIsNoOp(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("close twice"), domain.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := rh.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := rh.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// --- Offline blocks Get ---

func TestGet_BlockedInOffline(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("ok"), domain.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMaintenanceMode(context.Background(), domain.MaintenanceModeOffline); err != nil {
		t.Fatal(err)
	}
	_, err = s.Get(context.Background(), id, domain.GetOptions{})
	if !errors.Is(err, errs.ErrStoreOffline) {
		t.Fatalf("expected errs.ErrStoreOffline, got %v", err)
	}
}

func TestGet_AllowedInReadOnly(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("ok"), domain.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMaintenanceMode(context.Background(), domain.MaintenanceModeReadOnly); err != nil {
		t.Fatal(err)
	}
	rh, err := s.Get(context.Background(), id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get under ReadOnly should succeed: %v", err)
	}
	rh.Close()
}

func TestGet_CtxCancelled(t *testing.T) {
	s, _ := storefx.InitWithRoot(t)
	id, err := s.Put(context.Background(), payload("ok"), domain.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.Get(ctx, id, domain.GetOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
