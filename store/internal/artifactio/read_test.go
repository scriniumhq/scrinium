package artifactio_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
	"scrinium.dev/internal/testutil/artifactfx"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
	"scrinium.dev/store/artifact"
	"scrinium.dev/store/driver"
	"scrinium.dev/store/index"
	"scrinium.dev/store/internal/artifactio"
	"scrinium.dev/store/internal/storeconfig"
	"scrinium.dev/store/pipeline"
)

// rwHarness shares one (driver, index) pair between a Writer and a Reader,
// so an artifact written by the Writer can be read back by the Reader —
// the true round-trip.
func rwHarness(t *testing.T) (*artifactio.IO, *artifactio.IO, driver.Driver, index.StoreIndex, domain.StoreConfig) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	tr := pipeline.NewTransformerRegistry()
	w := artifactio.New(drv, idx, artifactfx.Hashes(), tr)
	r := artifactio.New(drv, idx, artifactfx.Hashes(), tr)
	cfg := storeconfig.ApplyDefaults(domain.StoreConfig{})
	return w, r, drv, idx, cfg
}

// write puts content through the Writer's three phases and returns the id.
func write(t *testing.T, w *artifactio.IO, cfg domain.StoreConfig, content string) domain.ArtifactID {
	t.Helper()
	ctx := context.Background()
	opts := domain.PutOptions{Namespace: "ns"}
	blob, err := w.Materialize(ctx, cfg, domain.Artifact{Payload: strings.NewReader(content)}, opts, "")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	m, mb, err := w.AssembleManifest(cfg, domain.Artifact{}, opts, blob, nil, "")
	if err != nil {
		t.Fatalf("AssembleManifest: %v", err)
	}
	if err := w.PersistManifest(ctx, m, mb, blob.Addr); err != nil {
		t.Fatalf("PersistManifest: %v", err)
	}
	return m.ArtifactID
}

// --- Load ---

func TestLoad_RoundTrip(t *testing.T) {
	w, r, _, _, cfg := rwHarness(t)
	id := write(t, w, cfg, "load me")

	m, err := r.Load(context.Background(), id, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if m.ArtifactID != id {
		t.Errorf("Load returned id %q, want %q", m.ArtifactID, id)
	}
	if m.LayoutHeader.BlobStorage != domain.LayoutTarget {
		t.Errorf("expected Target layout, got %q", m.LayoutHeader.BlobStorage)
	}
}

func TestLoad_MissingIsNotFound(t *testing.T) {
	_, r, _, _, _ := rwHarness(t)
	_, err := r.Load(context.Background(), domain.ArtifactID("sha256-"+strings.Repeat("f", 64)), nil)
	if err != errs.ErrArtifactNotFound {
		t.Fatalf("want ErrArtifactNotFound, got %v", err)
	}
}

func TestLoad_TamperedManifestCorrupted(t *testing.T) {
	w, r, drv, _, cfg := rwHarness(t)
	id := write(t, w, cfg, "tamper me")

	// Precondition: loads cleanly.
	if _, err := r.Load(context.Background(), id, nil); err != nil {
		t.Fatalf("precondition Load: %v", err)
	}
	// Overwrite the manifest file with garbage at its real path. Load must
	// reject it (ArtifactID hash check or decode), never silently succeed.
	mp, err := artifactManifestPath(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := drv.Put(context.Background(), mp, bytes.NewReader([]byte("\x00SC1\x00not-json"))); err != nil {
		t.Fatalf("overwrite manifest: %v", err)
	}
	if _, err := r.Load(context.Background(), id, nil); err == nil {
		t.Fatal("Load must fail on a tampered manifest file")
	}
}

// artifactManifestPath mirrors artifact.ManifestPath for the test (kept
// local to avoid an extra import line in the harness section).
func artifactManifestPath(id domain.ArtifactID) (string, error) {
	return artifact.ManifestPath(id)
}

// --- OpenBlob ---

func TestOpenBlob_TargetReadsBackContent(t *testing.T) {
	w, r, _, _, cfg := rwHarness(t)
	const content = "open and read these bytes"
	id := write(t, w, cfg, content)

	m, err := r.Load(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := r.OpenBlob(context.Background(), m)
	if err != nil {
		t.Fatalf("OpenBlob: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("OpenBlob content: got %q, want %q", got, content)
	}
}

func TestOpenBlob_Inline(t *testing.T) {
	w, r, _, _, cfg := rwHarness(t)
	cfg.BlobStorage = domain.BlobStorageInlineFallback
	cfg.InlineBlobLimit = 1024
	id := write(t, w, cfg, "inline content")

	m, err := r.Load(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	rc, err := r.OpenBlob(context.Background(), m)
	if err != nil {
		t.Fatalf("OpenBlob inline: %v", err)
	}
	defer rc.Close()
	got, _ := io.ReadAll(rc)
	if string(got) != "inline content" {
		t.Errorf("inline OpenBlob: got %q", got)
	}
}

// --- VerifyBlob ---

func TestVerifyBlob_Passes(t *testing.T) {
	w, r, _, _, cfg := rwHarness(t)
	id := write(t, w, cfg, "verify me clean")
	m, err := r.Load(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.VerifyBlob(context.Background(), m); err != nil {
		t.Fatalf("VerifyBlob on a clean artifact: %v", err)
	}
}

// --- OpenHandle + WrapVerifying (the domain.ReadHandle path) ---

func TestOpenHandle_TargetStreamsAndVerifies(t *testing.T) {
	w, r, _, _, cfg := rwHarness(t)
	const content = "stream through the verifying handle"
	id := write(t, w, cfg, content)
	m, err := r.Load(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}

	inner, err := r.OpenHandle(context.Background(), m)
	if err != nil {
		t.Fatalf("OpenHandle: %v", err)
	}
	vh, err := r.WrapVerifying(inner, nil)
	if err != nil {
		t.Fatalf("WrapVerifying: %v", err)
	}
	defer vh.Close()

	got, err := io.ReadAll(vh)
	if err != nil {
		t.Fatalf("streaming read through verifying handle should pass: %v", err)
	}
	if string(got) != content {
		t.Errorf("verifying handle content: got %q, want %q", got, content)
	}
	if !vh.SupportsRandomAccess() {
		t.Error("a Plain Target blob should support random access")
	}
	if vh.Manifest().ArtifactID != id {
		t.Error("handle.Manifest() lost the id")
	}
}

func TestWrapVerifying_NoContentHashReturnsInner(t *testing.T) {
	_, r, _, _, _ := rwHarness(t)
	inner := artifactio.NewInlineHandle(domain.Manifest{InlineBlob: []byte("x")}) // no ContentHash
	wrapped, err := r.WrapVerifying(inner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped != inner {
		t.Error("WrapVerifying should return inner unchanged when there is no ContentHash")
	}
}

// --- VerifyBlob detects a corrupted on-disk blob ---

func TestVerifyBlob_DetectsCorruption(t *testing.T) {
	w, r, drv, idx, cfg := rwHarness(t)
	id := write(t, w, cfg, "soon to be corrupt")
	m, err := r.Load(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Resolve the blob's physical path and overwrite it with wrong bytes.
	addr, err := idx.Resolve(context.Background(), string(m.BlobRef))
	if err != nil {
		t.Fatalf("resolve blob: %v", err)
	}
	if err := drv.Put(context.Background(), addr.Path, bytes.NewReader([]byte("CORRUPTED"))); err != nil {
		t.Fatalf("corrupt blob: %v", err)
	}
	if err := r.VerifyBlob(context.Background(), m); err == nil {
		t.Fatal("VerifyBlob must fail on a corrupted blob")
	}
}

// WrapVerifying must invoke onMismatch when streaming detects corruption —
// the hook the store uses to publish EventScrubFailed (the failure fires
// inside Read, after Get returned, so the store cannot observe it directly).
func TestWrapVerifying_OnMismatchFiresOnCorruption(t *testing.T) {
	w, r, drv, idx, cfg := rwHarness(t)
	id := write(t, w, cfg, "will be corrupted under a verifying handle")
	m, err := r.Load(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := idx.Resolve(context.Background(), string(m.BlobRef))
	if err != nil {
		t.Fatal(err)
	}
	if err := drv.Put(context.Background(), addr.Path, bytes.NewReader([]byte("garbage"))); err != nil {
		t.Fatal(err)
	}

	inner, err := r.OpenHandle(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	var firedID domain.ArtifactID
	var firedErr error
	calls := 0
	vh, err := r.WrapVerifying(inner, func(aid domain.ArtifactID, e error) {
		calls++
		firedID, firedErr = aid, e
	})
	if err != nil {
		t.Fatal(err)
	}
	defer vh.Close()

	_, _ = io.ReadAll(vh) // drives the stream to EOF, triggering finalize

	if calls != 1 {
		t.Fatalf("onMismatch fired %d times, want 1", calls)
	}
	if firedID != id {
		t.Errorf("onMismatch id: got %q, want %q", firedID, id)
	}
	if !errorsIsCorrupted(firedErr) {
		t.Errorf("onMismatch err: %v, want ErrCorruptedBlob", firedErr)
	}
}

func errorsIsCorrupted(err error) bool { return errors.Is(err, errs.ErrCorruptedBlob) }
