package store_test

import (
	"context"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/store"
	"scrinium.dev/internal/testutil/indexfx"
	"scrinium.dev/internal/testutil/storefx"
)

// mustWriteSysConfig writes a known StoreConfig through
// writeSystemConfig and returns the driver root, the config that
// was written, the resulting ArtifactID, and the bound index (so
// the caller can close it). Callers that don't need the index
// hand it straight to t.Cleanup.
func mustWriteSysConfig(t *testing.T) (string, domain.StoreConfig, domain.ArtifactID) {
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
		ManifestStorage:  domain.ManifestStorageLocal,
		ManifestCrypto:   domain.ManifestCryptoPlain,
	}

	id, err := store.WriteSystemConfig(context.Background(), drv, idx, storefx.Hashes(), cfg)
	if err != nil {
		t.Fatalf("WriteSystemConfig: %v", err)
	}
	if id == "" {
		t.Fatal("WriteSystemConfig returned empty ArtifactID")
	}
	return root, cfg, id
}

func TestWriteReadSystemConfig_RoundTrip(t *testing.T) {
	root, cfg, id := mustWriteSysConfig(t)

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

	// Pointer file holds exactly the ArtifactID followed by a newline.
	rc, err := drv.Get(context.Background(), store.SysConfigPointer)
	if err != nil {
		t.Fatalf("read pointer: %v", err)
	}
	defer rc.Close()
	buf := make([]byte, 512)
	n, _ := rc.Read(buf)
	if got, want := strings.TrimSpace(string(buf[:n])), string(id); got != want {
		t.Errorf("pointer content: got %q, want %q", got, want)
	}
}
