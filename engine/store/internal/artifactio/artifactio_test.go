package artifactio_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/pipeline"
	"scrinium.dev/engine/store/internal/artifactio"
	"scrinium.dev/engine/store/internal/storeconfig"
	"scrinium.dev/internal/testutil/artifactfx"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
)

// harness wires a Writer over a localfs driver, in-memory index, the
// artifactfx sha256 registry, and an empty transformer registry (no
// pipeline stages → Plain content).
func harness(t *testing.T) (*artifactio.Writer, domain.StoreConfig) {
	t.Helper()
	w := artifactio.New(
		driverfx.LocalFS(t),
		indexfx.Memory(t),
		artifactfx.Hashes(),
		pipeline.NewTransformerRegistry(),
	)
	cfg := storeconfig.ApplyDefaults(domain.StoreConfig{})
	return w, cfg
}

func payload(s string) domain.Artifact { return domain.Artifact{Payload: strings.NewReader(s)} }

// --- Target path: materialize → assemble → persist round-trip ---

func TestWritePath_TargetRoundTrip(t *testing.T) {
	w, cfg := harness(t)
	ctx := context.Background()

	blob, err := w.Materialize(ctx, cfg, payload("hello target"), domain.PutOptions{Namespace: "ns"}, "")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if blob.InlineBytes != nil {
		t.Error("Target config should not produce inline bytes")
	}
	if blob.ContentHash == "" || blob.BlobRef == "" {
		t.Fatal("Materialize produced empty addressing")
	}

	m, mb, err := w.AssembleManifest(cfg, payload(""), domain.PutOptions{Namespace: "ns"}, blob, nil, "")
	if err != nil {
		t.Fatalf("AssembleManifest: %v", err)
	}
	if m.ArtifactID == "" {
		t.Fatal("AssembleManifest did not assign an ArtifactID")
	}
	// The bytes must decode back to an equivalent manifest (Plain).
	got, err := artifact.Decode(mb)
	if err != nil {
		t.Fatalf("decode assembled manifest: %v", err)
	}
	if got.ContentHash != blob.ContentHash || got.LayoutHeader.BlobStorage != domain.LayoutTarget {
		t.Errorf("assembled manifest mismatch: %+v", got)
	}

	if err := w.PersistManifest(ctx, m, mb, blob.Addr); err != nil {
		t.Fatalf("PersistManifest: %v", err)
	}
}

// --- Inline path: small payload under InlineFallback limit ---

func TestWritePath_InlineUnderLimit(t *testing.T) {
	w, cfg := harness(t)
	cfg.BlobStorage = domain.BlobStorageInlineFallback
	cfg.InlineBlobLimit = 1024

	blob, err := w.Materialize(context.Background(), cfg, payload("tiny"), domain.PutOptions{}, "")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if blob.InlineBytes == nil {
		t.Fatal("payload under the inline limit should be inlined")
	}
	if !bytes.Equal(blob.InlineBytes, []byte("tiny")) {
		t.Errorf("inline bytes: got %q", blob.InlineBytes)
	}
	// Inline layout assembles to a LayoutInline manifest.
	m, _, err := w.AssembleManifest(cfg, payload(""), domain.PutOptions{}, blob, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.LayoutHeader.BlobStorage != domain.LayoutInline {
		t.Errorf("inline blob should produce LayoutInline, got %q", m.LayoutHeader.BlobStorage)
	}
}

// --- Inline fallback overflow: payload over the limit streams to Target ---

func TestWritePath_InlineOverflowStreamsToTarget(t *testing.T) {
	w, cfg := harness(t)
	cfg.BlobStorage = domain.BlobStorageInlineFallback
	cfg.InlineBlobLimit = 8

	big := strings.Repeat("x", 64) // > limit
	blob, err := w.Materialize(context.Background(), cfg, payload(big), domain.PutOptions{}, "")
	if err != nil {
		t.Fatalf("Materialize: %v", err)
	}
	if blob.InlineBytes != nil {
		t.Error("payload over the inline limit must stream to Target, not inline")
	}
}

// --- Dedup: identical Plain content shares one blob ---

func TestWritePath_DedupHitSharesBlob(t *testing.T) {
	w, cfg := harness(t)
	ctx := context.Background()
	const content = "dedup me"

	// First write commits a new blob and indexes it.
	b1, err := w.Materialize(ctx, cfg, payload(content), domain.PutOptions{Namespace: "ns"}, "")
	if err != nil {
		t.Fatal(err)
	}
	m1, mb1, _ := w.AssembleManifest(cfg, payload(""), domain.PutOptions{Namespace: "ns"}, b1, nil, "")
	if err := w.PersistManifest(ctx, m1, mb1, b1.Addr); err != nil {
		t.Fatal(err)
	}

	// Second write of identical content should dedup-hit: same BlobRef and
	// the same committed address as the first.
	b2, err := w.Materialize(ctx, cfg, payload(content), domain.PutOptions{Namespace: "ns"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if b2.BlobRef != b1.BlobRef {
		t.Errorf("dedup: BlobRef differs (%q vs %q)", b2.BlobRef, b1.BlobRef)
	}
	if b2.Addr.Path != b1.Addr.Path {
		t.Errorf("dedup: address differs (%q vs %q)", b2.Addr.Path, b1.Addr.Path)
	}
}

// --- BlobPath consistency: committed address matches the format layout ---

func TestWritePath_AddrMatchesBlobPath(t *testing.T) {
	w, cfg := harness(t)
	b, err := w.Materialize(context.Background(), cfg, payload("addr check"), domain.PutOptions{}, "")
	if err != nil {
		t.Fatal(err)
	}
	want, err := artifact.BlobPath(cfg.PathTopology, domain.BlobTypeRegular, string(b.BlobRef))
	if err != nil {
		t.Fatal(err)
	}
	if b.Addr.Path != want {
		t.Errorf("committed addr %q != artifact.BlobPath %q", b.Addr.Path, want)
	}
}
