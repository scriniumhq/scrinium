package storesuite

import (
	"context"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/store/internal/storeconfig"
	"scrinium.dev/testutil/storefx"
)

// mustWriteSysConfig writes a known StoreConfig through the system
// config writer and returns the driver root plus the config that was
// written. Under the seq model (ADR-85) the active config is simply the
// highest system/config version — there is no pointer file and no
// floating handle, so nothing identity-shaped is returned.
func mustWriteSysConfig(t *testing.T) (string, domain.StoreConfig) {
	t.Helper()
	root := t.TempDir()
	drv, err := localfs.New(root, localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}

	cfg := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashSHA256,
		ManifestEncoding: domain.ManifestEncodingJSON,
		ManifestCrypto:   domain.ManifestCryptoPlain,
	}

	if _, err := storeconfig.Write(context.Background(), drv, storefx.Hashes(), cfg); err != nil {
		t.Fatalf("WriteSystemConfig: %v", err)
	}
	return root, cfg
}

func TestWriteReadSystemConfig_RoundTrip(t *testing.T) {
	root, cfg := mustWriteSysConfig(t)

	drv, err := localfs.New(root, localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}

	got, _, err := storeconfig.Read(context.Background(), drv, storefx.Hashes())
	if err != nil {
		t.Fatalf("ReadSystemConfig: %v", err)
	}
	if got.PathTopology != cfg.PathTopology {
		t.Errorf("PathTopology: got %q, want %q", got.PathTopology, cfg.PathTopology)
	}
	if got.ContentHasher != cfg.ContentHasher {
		t.Errorf("ContentHasher: got %q, want %q", got.ContentHasher, cfg.ContentHasher)
	}
}
