package core_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver/localfs"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/testutil/indexfx"
	"github.com/rkurbatov/scrinium/internal/testutil/storefx"
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
	t.Cleanup(func() { _ = storefx.Close(idx) })

	cfg := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashSHA256,
		ManifestEncoding: domain.ManifestEncodingJSON,
		ManifestStorage:  domain.ManifestStorageLocal,
		ManifestCrypto:   domain.ManifestCryptoPlain,
	}

	id, err := core.WriteSystemConfig(context.Background(), drv, idx, storefx.Hashes(), cfg)
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

	got, err := core.ReadSystemConfig(context.Background(), drv, storefx.Hashes())
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
	rc, err := drv.Get(context.Background(), core.SysConfigPointer)
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

func TestReadSystemConfig_Missing(t *testing.T) {
	drv, err := localfs.New(t.TempDir(), localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}
	_, err = core.ReadSystemConfig(context.Background(), drv, storefx.Hashes())
	if !errors.Is(err, errs.ErrMissingConfigPointer) {
		t.Fatalf("expected ErrMissingConfigPointer, got %v", err)
	}
}

func TestReadSystemConfig_CorruptedPointer(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
	}{
		{"empty", []byte("")},
		{"whitespace only", []byte("   \n")},
		{"garbage", []byte("not-an-artifact-id\n")},
		{"missing dash", []byte("sha256deadbeef\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			drv, err := localfs.New(t.TempDir(), localfs.WithFsync(false))
			if err != nil {
				t.Fatalf("localfs.New: %v", err)
			}
			if err := drv.Put(context.Background(), core.SysConfigPointer,
				bytes.NewReader(c.content)); err != nil {
				t.Fatalf("seed pointer: %v", err)
			}
			_, err = core.ReadSystemConfig(context.Background(), drv, storefx.Hashes())
			if !errors.Is(err, errs.ErrCorruptedConfigPointer) {
				t.Fatalf("expected ErrCorruptedConfigPointer, got %v", err)
			}
		})
	}
}

func TestReadSystemConfig_DanglingPointer(t *testing.T) {
	drv, err := localfs.New(t.TempDir(), localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}
	// Syntactically valid ArtifactID, no manifest behind it.
	pointer := []byte("sha256-" +
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef\n")
	if err := drv.Put(context.Background(), core.SysConfigPointer,
		bytes.NewReader(pointer)); err != nil {
		t.Fatalf("seed pointer: %v", err)
	}
	_, err = core.ReadSystemConfig(context.Background(), drv, storefx.Hashes())
	if !errors.Is(err, errs.ErrDanglingConfigPointer) {
		t.Fatalf("expected ErrDanglingConfigPointer, got %v", err)
	}
}
