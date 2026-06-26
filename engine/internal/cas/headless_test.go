package cas_test

import (
	"context"
	"io"
	"strings"
	"testing"
)

// WriteHeadless stores a payload as a headless, blob-backed data artifact and
// OpenHandleByDigest resolves it straight back by ManifestDigest — the
// external_payload_ref round-trip a checkpoint pointer relies on (ADR-105).
// The artifact has no floating handle; the digest is the only reference.
func TestHeadless_WriteThenOpenByDigest_RoundTrip(t *testing.T) {
	w, r, _, _, cfg := rwHarness(t)
	ctx := context.Background()
	// Larger than the inline cap so the body is forced to a real blob, which
	// is the regime a checkpoint .db always lands in.
	content := "SQLite format 3\x00" + strings.Repeat("checkpoint-page-data;", 4096)

	digest, err := w.WriteHeadless(ctx, cfg, strings.NewReader(content), nil, "")
	if err != nil {
		t.Fatalf("WriteHeadless: %v", err)
	}
	if digest == "" {
		t.Fatal("WriteHeadless returned an empty digest")
	}

	rh, err := r.OpenHandleByDigest(ctx, digest, nil, string(cfg.ContentHasher))
	if err != nil {
		t.Fatalf("OpenHandleByDigest: %v", err)
	}
	defer rh.Close()

	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != content {
		t.Errorf("round-trip mismatch: got %d bytes, want %d", len(got), len(content))
	}
}
