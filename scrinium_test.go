package scrinium_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium"
	"github.com/rkurbatov/scrinium/engine/core"
	"github.com/rkurbatov/scrinium/engine/domain"

	// Importing scrinium already pulls in localfs and sqlite via
	// its own side-effect imports, but tests sometimes verify
	// schemes are actually wired. The double-import is a no-op
	// at runtime.
	_ "github.com/rkurbatov/scrinium/engine/driver/localfs"
	_ "github.com/rkurbatov/scrinium/engine/index/sqlite"
)

// initStorePlain initialises an empty Plain-DEK store at dir
// so scrinium.Open can pick it up. Tests that need a fresh
// store call this before scrinium.Open.
func initStorePlain(t *testing.T, dir string) {
	t.Helper()
	ctx := context.Background()

	drv, err := openLocalDriver(dir)
	if err != nil {
		t.Fatalf("init: open driver: %v", err)
	}
	idx, err := openLocalIndex(ctx, filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatalf("init: open index: %v", err)
	}
	defer idx.Close()

	if _, _, err := core.InitStore(ctx, drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(testHashRegistry()),
	); err != nil {
		t.Fatalf("init: %v", err)
	}
}

// TestOpenAndPut covers the round-trip: bring up scrinium against
// a fresh store, put a small artifact through the store API,
// retrieve it, close. Smallest end-to-end check that the
// bootstrap holds together — driver, index, view, fsops all
// wired.
func TestOpenAndPut(t *testing.T) {
	dir := t.TempDir()
	initStorePlain(t, dir)

	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + dir

	ctx := context.Background()
	s, err := scrinium.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Sanity: every required field populated.
	if s.Store == nil {
		t.Fatal("Scrinium.Store: nil")
	}
	if s.Index == nil {
		t.Fatal("Scrinium.Index: nil")
	}
	if s.View == nil {
		t.Fatal("Scrinium.View: nil")
	}
	if s.FSOps == nil {
		t.Fatal("Scrinium.FSOps: nil")
	}
	if s.MountSession == "" {
		t.Fatal("Scrinium.MountSession: empty")
	}

	// Put through the store, read back. We don't wire fsmeta
	// here — scrinium supports it but it's not required for
	// this smoke test.
	body := []byte("hello scrinium")
	id, err := s.Store.Put(ctx,
		domain.Artifact{Payload: bytes.NewReader(body)},
		domain.PutOptions{Namespace: "test"},
	)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rh, err := s.Store.Get(ctx, id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("payload mismatch: got %q want %q", got, body)
	}
}

// TestOpen_RejectsBadConfig covers the validation path:
// scrinium.Open must refuse obviously broken configs before
// doing any I/O.
func TestOpen_RejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  scrinium.Config
		want string
	}{
		{
			name: "empty store",
			cfg:  scrinium.Config{},
			want: "store: empty",
		},
		{
			name: "bad editing",
			cfg: scrinium.Config{
				Store:   "file:///tmp/x",
				Editing: "maybe",
			},
			want: "editing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := scrinium.Open(context.Background(), tc.cfg)
			if err == nil {
				t.Fatalf("Open: expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

// TestOpen_DefaultsIndex verifies the convenience: when Index
// is empty and Store is local, scrinium synthesises a sqlite://
// URI under the store directory.
func TestOpen_DefaultsIndex(t *testing.T) {
	dir := t.TempDir()
	initStorePlain(t, dir)

	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + dir
	cfg.Index = "" // explicit: rely on the default

	ctx := context.Background()
	s, err := scrinium.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	idxPath := filepath.Join(dir, "index.db")
	if _, err := readFile(idxPath); err != nil {
		t.Errorf("expected sqlite at %q: %v", idxPath, err)
	}
}

// --- Init tests ---

// TestInit_PlainStore_RoundTrip exercises Init on a fresh
// directory: it must create the store, return a usable
// runtime, and that runtime must round-trip a Put/Get.
func TestInit_PlainStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + dir

	ctx := context.Background()
	s, kit, err := scrinium.Init(ctx, cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()

	if kit != nil {
		t.Errorf("Plain store Init: unexpected recovery kit (%d bytes)", len(kit))
	}
	if s.Store == nil {
		t.Fatal("post-Init: Store nil")
	}

	// Round-trip a put/get to prove the store is usable.
	body := []byte("init smoke")
	id, err := s.Store.Put(ctx,
		domain.Artifact{Payload: bytes.NewReader(body)},
		domain.PutOptions{Namespace: "init"},
	)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	rh, err := s.Store.Get(ctx, id, domain.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rh.Close()
	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("payload mismatch: got %q want %q", got, body)
	}
}

// TestInit_CreatesDirectory verifies Init mkdirs the store
// path when it does not exist. Useful for first-run UX:
// pointing at a nonexistent path Just Works.
func TestInit_CreatesDirectory(t *testing.T) {
	parent := t.TempDir()
	storeDir := filepath.Join(parent, "fresh-store")

	cfg := scrinium.DefaultConfig()
	cfg.Store = "file://" + storeDir

	ctx := context.Background()
	s, _, err := scrinium.Init(ctx, cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer s.Close()

	if _, err := readFile(filepath.Join(storeDir, "index.db")); err != nil {
		t.Errorf("expected index.db in %q: %v", storeDir, err)
	}
}
