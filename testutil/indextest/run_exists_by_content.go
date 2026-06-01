package indextest

import (
	"testing"

	"scrinium.dev/testutil/manifestfx"
)

// --- ExistsByContent ---

func runExistsByContent(t *testing.T, f Factory) {
	t.Run("Hit", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('a')
		m := manifestfx.BlobWithHash("art-1", "blob-1", hash, 1024)
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("blobs/blob-1"), nil, nil); err != nil {
			t.Fatalf("IndexManifest: %v", err)
		}

		ref, ok, err := idx.ExistsByContent(ctx, hash, 1024, "")
		if err != nil {
			t.Fatalf("ExistsByContent: %v", err)
		}
		if !ok {
			t.Fatal("expected found")
		}
		if ref != "blob-1" {
			t.Errorf("ref: got %q, want %q", ref, "blob-1")
		}
	})

	t.Run("Miss", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		ref, ok, err := idx.ExistsByContent(ctx, "sha256-deadbeef", 999, "")
		if err != nil {
			t.Fatalf("ExistsByContent: %v", err)
		}
		if ok {
			t.Error("expected not found")
		}
		if ref != "" {
			t.Errorf("ref: got %q, want empty", ref)
		}
	})

	t.Run("HashHitSizeMiss", func(t *testing.T) {
		ctx := t.Context()
		// The composite key (content_hash, original_size) is
		// strict: same hash, different size — distinct entries,
		// not matches.
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('x')
		m := manifestfx.BlobWithHash("art-1k", "blob-1k", hash, 1024)
		if err := idx.IndexManifest(ctx, m, manifestfx.PhysAddr("p1"), nil, nil); err != nil {
			t.Fatal(err)
		}

		_, ok, err := idx.ExistsByContent(ctx, hash, 2048, "")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("hash-only match leaked through size filter")
		}
	})

	t.Run("CryptoIdentitySplitsKey", func(t *testing.T) {
		ctx := t.Context()
		idx := f.New(t)
		hash := manifestfx.SyntheticHash('c')
		// Same plaintext (hash,size) but two distinct crypto
		// identities must be two independent rows — ADR-58.
		mPlain := manifestfx.BlobWithHash("art-plain", "blob-plain", hash, 2048)
		mEnc := manifestfx.EncryptedBlobWithHash("art-enc", "blob-enc", hash, 2048, "aes-gcm", "k1")
		if err := idx.IndexManifest(ctx, mPlain, manifestfx.PhysAddr("p-plain"), nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := idx.IndexManifest(ctx, mEnc, manifestfx.PhysAddr("p-enc"), nil, nil); err != nil {
			t.Fatal(err)
		}

		// Plain probe finds only the plain blob.
		ref, ok, err := idx.ExistsByContent(ctx, hash, 2048, "")
		if err != nil || !ok || ref != "blob-plain" {
			t.Fatalf("plain probe: ref=%q ok=%v err=%v", ref, ok, err)
		}
		// Encrypted probe finds only the encrypted blob.
		ref, ok, err = idx.ExistsByContent(ctx, hash, 2048, "aes-gcm/k1")
		if err != nil || !ok || ref != "blob-enc" {
			t.Fatalf("encrypted probe: ref=%q ok=%v err=%v", ref, ok, err)
		}
		// A different KeyID is a miss — never collapses.
		_, ok, err = idx.ExistsByContent(ctx, hash, 2048, "aes-gcm/k2")
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Error("different KeyID must not match")
		}
	})
}
