// SystemStore behaviour (single implementation, so this is a plain
// behaviour suite, not a conformance suite per TESTING.md §3). Covers the
// versioned named address space (ADR-85) and the keep=0 exclusive-cell
// routing (ADR-100/101): put/get/update/delete, prefix Walk, the
// not-indexed invariant, name validation, and cell/versioned coexistence.
//
// State-only prefixes ("scrub/", "celltest/") are never touched by store
// init, so Walk assertions under them can be absolute; the empty-prefix
// case asserts on the delta instead.

package storesuite

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/systemstore"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

// TestSystemStore_VersionedPutGet: a default (versioned) Put round-trips,
// and a second Put under the same name supersedes the first — Get returns
// the latest payload.
func TestSystemStore_VersionedPutGet(t *testing.T) {
	t.Run("round-trip", func(t *testing.T) {
		ss, _ := storefx.InitPlainSystem(t)
		ctx := context.Background()
		body := []byte("hello cursor 2026-04-01T12:00:00Z")
		if err := ss.Put(ctx, systemstore.NamedArtifact{Name: "scrub/cursor", Payload: bytes.NewReader(body)}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		rh, err := ss.Get(ctx, "scrub/cursor")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer rh.Close()
		got, err := io.ReadAll(rh)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("payload round-trip: got %q, want %q", got, body)
		}
	})

	t.Run("update replaces predecessor", func(t *testing.T) {
		ss, _ := storefx.InitPlainSystem(t)
		ctx := context.Background()
		v1 := []byte("version-1")
		v2 := []byte("version-2-newer")
		if err := ss.Put(ctx, systemstore.NamedArtifact{Name: "scrub/cursor", Payload: bytes.NewReader(v1)}); err != nil {
			t.Fatalf("Put v1: %v", err)
		}
		if err := ss.Put(ctx, systemstore.NamedArtifact{Name: "scrub/cursor", Payload: bytes.NewReader(v2)}); err != nil {
			t.Fatalf("Put v2: %v", err)
		}
		rh, err := ss.Get(ctx, "scrub/cursor")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer rh.Close()
		got, _ := io.ReadAll(rh)
		if !bytes.Equal(got, v2) {
			t.Errorf("after update: got %q, want %q (v2)", got, v2)
		}
	})
}

// TestSystemStore_GetAbsentReturnsNotFound: a name never written is
// ErrArtifactNotFound.
func TestSystemStore_GetAbsentReturnsNotFound(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	_, err := ss.Get(context.Background(), "gc/cursor-never-written")
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("Get absent: got %v, want errs.ErrArtifactNotFound", err)
	}
}

// TestSystemStore_Delete: Delete is idempotent on a versioned name (absent
// is a no-op, post-Put Get is NotFound, a second Delete still succeeds) and
// also clears a keep=0 cell.
func TestSystemStore_Delete(t *testing.T) {
	t.Run("idempotent on versioned name", func(t *testing.T) {
		ss, _ := storefx.InitPlainSystem(t)
		ctx := context.Background()

		if err := ss.Delete(ctx, "scrub/cursor"); err != nil {
			t.Errorf("Delete absent: got %v, want nil", err)
		}
		if err := ss.Put(ctx, systemstore.NamedArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("x"))}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := ss.Delete(ctx, "scrub/cursor"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := ss.Get(ctx, "scrub/cursor"); !errors.Is(err, errs.ErrArtifactNotFound) {
			t.Errorf("Get after Delete: got %v, want errs.ErrArtifactNotFound", err)
		}
		if err := ss.Delete(ctx, "scrub/cursor"); err != nil {
			t.Errorf("Delete twice: got %v, want nil", err)
		}
	})

	t.Run("clears a keep=0 cell", func(t *testing.T) {
		ss, _ := storefx.InitPlainSystem(t)
		ctx := context.Background()
		if err := ss.Put(ctx, systemstore.NamedArtifact{
			Name: "celltest/lease", Payload: bytes.NewReader([]byte("x")), Keep: systemstore.KeepCell(),
		}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if err := ss.Delete(ctx, "celltest/lease"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := ss.Get(ctx, "celltest/lease"); !errors.Is(err, errs.ErrArtifactNotFound) {
			t.Errorf("Get after Delete cell: got %v, want errs.ErrArtifactNotFound", err)
		}
	})
}

// TestSystemStore_WalkByPrefix: Walk yields exactly the names under the
// requested prefix.
func TestSystemStore_WalkByPrefix(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()
	for _, name := range []string{
		"scrub/cursor",
		"scrub/last-failed",
		"gc/cursor",
		"snapshot/2026-04-01",
	} {
		if err := ss.Put(ctx, systemstore.NamedArtifact{Name: name, Payload: bytes.NewReader([]byte(name))}); err != nil {
			t.Fatalf("Put %q: %v", name, err)
		}
	}

	got := storekit.WalkNames(t, ss, "scrub/")
	sort.Strings(got)
	want := []string{"scrub/cursor", "scrub/last-failed"}
	if !slices.Equal(got, want) {
		t.Errorf("Walk(scrub/): got %v, want %v", got, want)
	}
}

// TestSystemStore_WalkEmptyPrefixScansAll: an empty prefix walks everything;
// a fresh store already exposes its initial config version, so the assertion
// is on the delta.
func TestSystemStore_WalkEmptyPrefixScansAll(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()

	before := storekit.WalkNames(t, ss, "")

	for _, n := range []string{"config/v1", "scrub/cursor", "gc/cursor"} {
		if err := ss.Put(ctx, systemstore.NamedArtifact{Name: n, Payload: bytes.NewReader([]byte(n))}); err != nil {
			t.Fatalf("Put %q: %v", n, err)
		}
	}

	got := setDiff(storekit.WalkNames(t, ss, ""), before)
	sort.Strings(got)
	want := []string{"config/v1", "gc/cursor", "scrub/cursor"}
	if !slices.Equal(got, want) {
		t.Errorf(`Walk("") delta: got %v, want %v`, got, want)
	}
}

// TestSystemStore_PutDoesNotIndex: system artifacts round-trip by name but
// are never added to the manifest index (ADR-85).
func TestSystemStore_PutDoesNotIndex(t *testing.T) {
	ss, idx := storefx.InitPlainSystem(t)
	ctx := context.Background()
	body := []byte("snapshot-payload-1234")

	before := countManifests(t, idx)

	if err := ss.Put(ctx, systemstore.NamedArtifact{Name: "index_checkpoint/2026-04-01", Payload: bytes.NewReader(body)}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	rh, err := ss.Get(ctx, "index_checkpoint/2026-04-01")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rh)
	_ = rh.Close()
	if !bytes.Equal(got, body) {
		t.Errorf("payload: got %q, want %q", got, body)
	}

	if d := countManifests(t, idx) - before; d != 0 {
		t.Errorf("system artifact appeared in index (delta %d, want 0)", d)
	}
}

// TestSystemStore_InvalidNamesRejected: malformed names are refused with
// ErrInvalidSystemName.
func TestSystemStore_InvalidNamesRejected(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()
	for _, name := range []string{
		"",
		"/leading",
		"trailing/",
		"double//slash",
		"a/./b",
		"a/../b",
	} {
		err := ss.Put(ctx, systemstore.NamedArtifact{Name: name, Payload: bytes.NewReader([]byte("x"))})
		if !errors.Is(err, errs.ErrInvalidSystemName) {
			t.Errorf("Put(%q): got %v, want errs.ErrInvalidSystemName", name, err)
		}
	}
}

// TestSystemStore_CellPutGet: a keep=0 cell round-trips, and a second Put
// overwrites in place (last-write-wins, no version history).
func TestSystemStore_CellPutGet(t *testing.T) {
	t.Run("round-trip", func(t *testing.T) {
		ss, _ := storefx.InitPlainSystem(t)
		ctx := context.Background()
		body := []byte("lease-holder-A")
		if err := ss.Put(ctx, systemstore.NamedArtifact{
			Name: "celltest/lease", Payload: bytes.NewReader(body), Keep: systemstore.KeepCell(),
		}); err != nil {
			t.Fatalf("Put cell: %v", err)
		}
		rh, err := ss.Get(ctx, "celltest/lease")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer rh.Close()
		got, err := io.ReadAll(rh)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("cell round-trip: got %q, want %q", got, body)
		}
	})

	t.Run("overwrite replaces in place", func(t *testing.T) {
		ss, _ := storefx.InitPlainSystem(t)
		ctx := context.Background()
		put := func(b string) {
			t.Helper()
			if err := ss.Put(ctx, systemstore.NamedArtifact{
				Name: "celltest/lease", Payload: bytes.NewReader([]byte(b)), Keep: systemstore.KeepCell(),
			}); err != nil {
				t.Fatalf("Put %q: %v", b, err)
			}
		}
		put("A")
		put("B") // overwrite in place — keep=0 is last-write-wins

		rh, err := ss.Get(ctx, "celltest/lease")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		defer rh.Close()
		got, _ := io.ReadAll(rh)
		if !bytes.Equal(got, []byte("B")) {
			t.Errorf("cell overwrite: got %q, want B", got)
		}
	})
}

// TestSystemStore_CellAndVersionedGetRoute: a cell and a versioned name
// coexist, and Get resolves each to its own form.
func TestSystemStore_CellAndVersionedGetRoute(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()

	if err := ss.Put(ctx, systemstore.NamedArtifact{
		Name: "celltest/lease", Payload: bytes.NewReader([]byte("cell")), Keep: systemstore.KeepCell(),
	}); err != nil {
		t.Fatalf("Put cell: %v", err)
	}
	if err := ss.Put(ctx, systemstore.NamedArtifact{
		Name: "celltest/cfg", Payload: bytes.NewReader([]byte("ver")), Keep: systemstore.KeepVersions(2),
	}); err != nil {
		t.Fatalf("Put versioned: %v", err)
	}

	for name, want := range map[string]string{"celltest/lease": "cell", "celltest/cfg": "ver"} {
		rh, err := ss.Get(ctx, name)
		if err != nil {
			t.Fatalf("Get %q: %v", name, err)
		}
		got, _ := io.ReadAll(rh)
		rh.Close()
		if string(got) != want {
			t.Errorf("Get %q: got %q, want %q", name, got, want)
		}
	}
}

// TestSystemStore_WalkIncludesCells: Walk surfaces both versioned actives
// and keep=0 cells under a prefix.
func TestSystemStore_WalkIncludesCells(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()

	if err := ss.Put(ctx, systemstore.NamedArtifact{
		Name: "celltest/lease", Payload: bytes.NewReader([]byte("L")), Keep: systemstore.KeepCell(),
	}); err != nil {
		t.Fatalf("Put cell: %v", err)
	}
	if err := ss.Put(ctx, systemstore.NamedArtifact{
		Name: "celltest/cfg", Payload: bytes.NewReader([]byte("C")), // nil Keep → default keep=1 (versioned)
	}); err != nil {
		t.Fatalf("Put versioned: %v", err)
	}

	got := storekit.WalkNames(t, ss, "celltest/")
	sort.Strings(got)
	want := []string{"celltest/cfg", "celltest/lease"}
	if !slices.Equal(got, want) {
		t.Errorf("Walk(celltest/): got %v, want %v (cell must be included)", got, want)
	}
}

// --- helpers ---

// countManifests returns the number of user manifest rows in the index.
func countManifests(t *testing.T, idx index.StoreIndex) int {
	t.Helper()
	var n int
	err := idx.IterateManifests(context.Background(), func(_ domain.Manifest) error {
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("IterateManifests: %v", err)
	}
	return n
}

// setDiff returns elements of got that are not in base.
func setDiff(got, base []string) []string {
	seen := make(map[string]struct{}, len(base))
	for _, b := range base {
		seen[b] = struct{}{}
	}
	var out []string
	for _, g := range got {
		if _, ok := seen[g]; !ok {
			out = append(out, g)
		}
	}
	return out
}
