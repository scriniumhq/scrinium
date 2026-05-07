package daemon_test

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/internal/daemon"

	// Importing daemon already pulls in localfs and sqlite via
	// its own side-effect imports, but tests sometimes verify
	// schemes are actually wired. The double-import is a no-op
	// at runtime.
	_ "github.com/rkurbatov/scrinium/driver/localfs"
	_ "github.com/rkurbatov/scrinium/index/sqlite"
)

// initStore initialises an empty Plain-DEK store at dir so the
// daemon can open it. We can't simply call daemon.Open against
// an empty directory: OpenStore expects an already-init'd
// system.config. The reference daemon's "init" subcommand does
// this for the CLI; here we replicate it in-process.
func initStore(t *testing.T, dir string) {
	t.Helper()
	ctx := context.Background()

	// Minimal local init via the public packages.
	drvAbs := dir
	drv, err := openLocalDriver(drvAbs)
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

// TestOpenAndPut covers the round-trip: bring up the daemon
// against a fresh store, put a small artifact through the
// store API, retrieve it via the daemon's View, close. This
// is the smallest end-to-end check that the bootstrap holds
// together — driver, index, view, fsops all wired.
func TestOpenAndPut(t *testing.T) {
	dir := t.TempDir()
	initStore(t, dir)

	cfg := daemon.DefaultConfig()
	cfg.Store = "file://" + dir
	// Index defaults to sqlite under store dir; let it.

	ctx := context.Background()
	d, err := daemon.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// Sanity: every required field populated.
	if d.Store == nil {
		t.Fatal("Daemon.Store: nil")
	}
	if d.Index == nil {
		t.Fatal("Daemon.Index: nil")
	}
	if d.View == nil {
		t.Fatal("Daemon.View: nil")
	}
	if d.FSOps == nil {
		t.Fatal("Daemon.FSOps: nil")
	}
	if d.MountSession == "" {
		t.Fatal("Daemon.MountSession: empty")
	}

	// Put an artifact through the store. We don't wire fsmeta
	// here — the daemon supports it but it's not required for
	// this smoke test. The artifact lives on without a
	// by-path placement.
	body := []byte("hello scrinium")
	id, err := d.Store.Put(ctx,
		domain.Artifact{Payload: bytes.NewReader(body)},
		domain.PutOptions{Namespace: "test"},
	)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Read back through the store. We use the store directly
	// rather than the View because the View needs an fsmeta-
	// sourced path to expose the artifact in by-path; our Put
	// didn't include one, so it's reachable only by id.
	rh, err := d.Store.Get(ctx, id, domain.GetOptions{})
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

// TestOpen_RejectsBadConfig covers the validation path: the
// daemon must refuse obviously broken configs before doing any
// I/O.
func TestOpen_RejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  daemon.Config
		want string
	}{
		{
			name: "empty store",
			cfg:  daemon.Config{},
			want: "store: empty",
		},
		{
			name: "bad editing",
			cfg: daemon.Config{
				Store:   "file:///tmp/x",
				Editing: "maybe",
			},
			want: "editing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := daemon.Open(context.Background(), tc.cfg)
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
// is empty and Store is local, the daemon synthesises a
// sqlite:// URI under the store directory.
func TestOpen_DefaultsIndex(t *testing.T) {
	dir := t.TempDir()
	initStore(t, dir)

	cfg := daemon.DefaultConfig()
	cfg.Store = "file://" + dir
	cfg.Index = "" // explicit: rely on the default

	ctx := context.Background()
	d, err := daemon.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// We can't introspect the URI used inside the daemon,
	// but we can check the index file was created next to
	// the store.
	idxPath := filepath.Join(dir, "index.db")
	if _, err := readFile(idxPath); err != nil {
		t.Errorf("expected sqlite at %q: %v", idxPath, err)
	}
}
