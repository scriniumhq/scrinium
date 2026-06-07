package store_test

import (
	"context"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/store"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// mustWriteSysConfig writes a known StoreConfig through
// writeSystemConfig and returns the driver root, the config that
// was written, and the resulting ManifestDigest (system artifacts are
// addressed by digest — the on-disk filename — not by a floating handle).
// The bound index is registered with t.Cleanup.
func mustWriteSysConfig(t *testing.T) (string, domain.StoreConfig, domain.ManifestDigest) {
	t.Helper()
	root := t.TempDir()
	drv, err := localfs.New(root, localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}
	idx := indexfx.Memory(t)
	t.Cleanup(func() { _ = idx.Close() })

	cfg := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashSHA256,
		ManifestEncoding: domain.ManifestEncodingJSON,
		ManifestCrypto:   domain.ManifestCryptoPlain,
	}

	digest, err := store.WriteSystemConfig(context.Background(), drv, idx, storefx.Hashes(), cfg)
	if err != nil {
		t.Fatalf("WriteSystemConfig: %v", err)
	}
	if digest == "" {
		t.Fatal("WriteSystemConfig returned empty ManifestDigest")
	}
	return root, cfg, digest
}

func TestWriteReadSystemConfig_RoundTrip(t *testing.T) {
	root, cfg, digest := mustWriteSysConfig(t)

	drv, err := localfs.New(root, localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}

	got, err := store.ReadSystemConfig(context.Background(), drv, storefx.Hashes())
	if err != nil {
		t.Fatalf("ReadSystemConfig: %v", err)
	}
	if got.PathTopology != cfg.PathTopology {
		t.Errorf("PathTopology: got %q, want %q", got.PathTopology, cfg.PathTopology)
	}
	if got.ContentHasher != cfg.ContentHasher {
		t.Errorf("ContentHasher: got %q, want %q", got.ContentHasher, cfg.ContentHasher)
	}

	// Pointer file holds exactly the ManifestDigest followed by a newline.
	rc, err := drv.Get(context.Background(), store.SysConfigPointer)
	if err != nil {
		t.Fatalf("read pointer: %v", err)
	}
	defer rc.Close()
	buf := make([]byte, 512)
	n, _ := rc.Read(buf)
	if got, want := strings.TrimSpace(string(buf[:n])), string(digest); got != want {
		t.Errorf("pointer content: got %q, want %q", got, want)
	}
}
