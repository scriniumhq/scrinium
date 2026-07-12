package store_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/event"
	"scrinium.dev/testutil/eventfx"
	"scrinium.dev/testutil/storefx"
)

// E2E coverage for the liveness sentinel (ADR-111): a real store on a
// real localfs root, a fast-ticking probe, and the physical world
// yanked out from under the instance the way the original incident
// did (store deleted while webview kept serving from the index).

const probeTick = 20 * time.Millisecond

// livenessFixture opens a real store with a fast sentinel and an event
// recorder, and knows how to find, remove and restore the descriptor
// cells on disk.
type livenessFixture struct {
	st   store.Store
	rec  *eventfx.Recorder
	root string
	// saved descriptor files: root-relative path → content
	saved map[string][]byte
}

func newLivenessFixture(t *testing.T) *livenessFixture {
	t.Helper()
	rec := eventfx.New()
	st, drv, _ := storefx.InitShared(t,
		store.WithPublisher(rec),
		store.WithLivenessInterval(probeTick),
	)
	t.Cleanup(func() { _ = st.Close() })
	return &livenessFixture{st: st, rec: rec, root: drv.Root(), saved: map[string][]byte{}}
}

// descriptorFiles walks a root and returns root-relative paths of every
// descriptor cell file (primary and backup, any version).
func descriptorFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.Contains(p, "store.descriptor") {
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(out) == 0 {
		t.Fatal("no descriptor cells found on disk")
	}
	return out
}

// removeDescriptor deletes every descriptor cell, remembering contents
// for a later restore.
func (f *livenessFixture) removeDescriptor(t *testing.T) {
	t.Helper()
	for _, rel := range descriptorFiles(t, f.root) {
		p := filepath.Join(f.root, rel)
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		f.saved[rel] = b
		if err := os.Remove(p); err != nil {
			t.Fatalf("remove %s: %v", rel, err)
		}
	}
}

// restoreDescriptor writes the saved cells back.
func (f *livenessFixture) restoreDescriptor(t *testing.T) {
	t.Helper()
	for rel, b := range f.saved {
		p := filepath.Join(f.root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatalf("restore %s: %v", rel, err)
		}
	}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(probeTick / 2)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func (f *livenessFixture) offline() bool {
	_, err := f.st.Capacity(context.Background())
	return errors.Is(err, errs.ErrStoreOffline)
}

func (f *livenessFixture) online() bool {
	_, err := f.st.Capacity(context.Background())
	return err == nil
}

// TestLiveness_LossFlipsOffline: descriptor gone → every gated
// operation answers ErrStoreOffline within a tick, and the degradation
// is published.
func TestLiveness_LossFlipsOffline(t *testing.T) {
	f := newLivenessFixture(t)

	if !f.online() {
		t.Fatal("fresh store must be operational")
	}
	f.removeDescriptor(t)

	eventually(t, "store to go offline after descriptor loss", f.offline)
	eventually(t, "EventStoreDegraded to be published", func() bool {
		return f.rec.Count(event.EventStoreDegraded) > 0
	})
}

// TestLiveness_SelfHealsOnSameStore: the same descriptor comes back
// (transient mount blip) → the sentinel lifts its own Offline and
// publishes recovery.
func TestLiveness_SelfHealsOnSameStore(t *testing.T) {
	f := newLivenessFixture(t)

	f.removeDescriptor(t)
	eventually(t, "store to go offline", f.offline)

	f.restoreDescriptor(t)
	eventually(t, "store to self-heal after descriptor return", f.online)
	eventually(t, "EventStoreRecovered to be published", func() bool {
		return f.rec.Count(event.EventStoreRecovered) > 0
	})
}

// TestLiveness_SubstitutionIsSticky: a DIFFERENT store's descriptor at
// our path → Offline; and even the original descriptor returning does
// NOT heal it — trust is re-established only by reopening (ADR-111,
// INV-111-3/4).
func TestLiveness_SubstitutionIsSticky(t *testing.T) {
	f := newLivenessFixture(t)

	// A second, foreign store: its descriptor cells land at the same
	// relative paths.
	_, foreignDrv, _ := storefx.InitShared(t)
	foreignRoot := foreignDrv.Root()

	// Remember our own cells, then overwrite them with the foreign ones.
	f.removeDescriptor(t)
	for _, rel := range descriptorFiles(t, foreignRoot) {
		b, err := os.ReadFile(filepath.Join(foreignRoot, rel))
		if err != nil {
			t.Fatalf("read foreign %s: %v", rel, err)
		}
		p := filepath.Join(f.root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatalf("plant foreign %s: %v", rel, err)
		}
	}

	eventually(t, "store to go offline on substitution", f.offline)

	// The original comes back — still offline: substitution is sticky.
	for _, rel := range descriptorFiles(t, f.root) {
		_ = os.Remove(filepath.Join(f.root, rel))
	}
	f.restoreDescriptor(t)

	// Give the sentinel several ticks to (wrongly) heal, then assert
	// it did not.
	time.Sleep(10 * probeTick)
	if !f.offline() {
		t.Fatal("substitution must be sticky: original descriptor returning must not heal the instance")
	}
}

// TestLiveness_OperatorOfflineIsNotTouched: an Offline set by the
// operator survives healthy probes — the sentinel manages only its own
// transitions (ADR-111, INV-111-4).
func TestLiveness_OperatorOfflineIsNotTouched(t *testing.T) {
	f := newLivenessFixture(t)
	ctx := context.Background()

	if err := f.st.SetMaintenanceMode(ctx, domain.MaintenanceModeOffline); err != nil {
		t.Fatalf("SetMaintenanceMode(Offline): %v", err)
	}
	// The descriptor is intact, probes report healthy — the sentinel
	// must NOT lift an operator-set Offline.
	time.Sleep(10 * probeTick)
	if !f.offline() {
		t.Fatal("operator Offline was lifted by the sentinel")
	}
	if err := f.st.SetMaintenanceMode(ctx, domain.MaintenanceModeNone); err != nil {
		t.Fatalf("SetMaintenanceMode(None): %v", err)
	}
	eventually(t, "store back online after operator lifts Offline", f.online)
}
