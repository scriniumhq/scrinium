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

func TestSystemStore_PutGetRoundTrip(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()
	body := []byte("hello cursor 2026-04-01T12:00:00Z")

	if err := ss.Put(ctx, systemstore.Artifact{Name: "scrub/cursor", Payload: bytes.NewReader(body)}); err != nil {
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
}

func TestSystemStore_PutUpdateReplacesPredecessor(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()
	v1 := []byte("version-1")
	v2 := []byte("version-2-newer")

	if err := ss.Put(ctx, systemstore.Artifact{Name: "scrub/cursor", Payload: bytes.NewReader(v1)}); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := ss.Put(ctx, systemstore.Artifact{Name: "scrub/cursor", Payload: bytes.NewReader(v2)}); err != nil {
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
}

func TestSystemStore_GetAbsentReturnsNotFound(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	_, err := ss.Get(context.Background(), "gc/cursor-never-written")
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("Get absent: got %v, want errs.ErrArtifactNotFound", err)
	}
}

func TestSystemStore_DeleteIdempotent(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()

	// Delete on absent — no-op.
	if err := ss.Delete(ctx, "scrub/cursor"); err != nil {
		t.Errorf("Delete absent: got %v, want nil", err)
	}

	// Put then Delete then Get returns NotFound.
	if err := ss.Put(ctx, systemstore.Artifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("x"))}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := ss.Delete(ctx, "scrub/cursor"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := ss.Get(ctx, "scrub/cursor"); !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("Get after Delete: got %v, want errs.ErrArtifactNotFound", err)
	}

	// Second Delete on same name — still no error.
	if err := ss.Delete(ctx, "scrub/cursor"); err != nil {
		t.Errorf("Delete twice: got %v, want nil", err)
	}
}

func TestSystemStore_WalkByPrefix(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()
	for _, name := range []string{
		"scrub/cursor",
		"scrub/last-failed",
		"gc/cursor",
		"snapshot/2026-04-01",
	} {
		if err := ss.Put(ctx, systemstore.Artifact{Name: name, Payload: bytes.NewReader([]byte(name))}); err != nil {
			t.Fatalf("Put %q: %v", name, err)
		}
	}

	// scrub/ is state-only and never touched by init, so an absolute
	// assertion is safe here (unlike the empty-prefix case below).
	got := storekit.WalkNames(t, ss, "scrub/")
	sort.Strings(got)
	want := []string{"scrub/cursor", "scrub/last-failed"}
	if !slices.Equal(got, want) {
		t.Errorf("Walk(scrub/): got %v, want %v", got, want)
	}
}

func TestSystemStore_WalkEmptyPrefixScansAll(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()

	// Baseline: a freshly initialised store already exposes
	// its initial config version through Walk. Assert on the delta.
	before := storekit.WalkNames(t, ss, "")

	for _, n := range []string{"config/v1", "scrub/cursor", "gc/cursor"} {
		if err := ss.Put(ctx, systemstore.Artifact{Name: n, Payload: bytes.NewReader([]byte(n))}); err != nil {
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

func TestSystemStore_PutDoesNotIndex(t *testing.T) {
	ss, idx := storefx.InitPlainSystem(t)
	ctx := context.Background()
	body := []byte("snapshot-payload-1234")

	before := countManifests(t, idx)

	if err := ss.Put(ctx, systemstore.Artifact{Name: "index_checkpoint/2026-04-01", Payload: bytes.NewReader(body)}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Get round-trips: system artifacts live in their own named/
	// address space, read back by name → seq → file.
	rh, err := ss.Get(ctx, "index_checkpoint/2026-04-01")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, _ := io.ReadAll(rh)
	_ = rh.Close()
	if !bytes.Equal(got, body) {
		t.Errorf("payload: got %q, want %q", got, body)
	}

	// System artifacts are never indexed (ADR-85): the index must not grow.
	if d := countManifests(t, idx) - before; d != 0 {
		t.Errorf("system artifact appeared in index (delta %d, want 0)", d)
	}
}

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
		err := ss.Put(ctx, systemstore.Artifact{Name: name, Payload: bytes.NewReader([]byte("x"))})
		if !errors.Is(err, errs.ErrInvalidSystemName) {
			t.Errorf("Put(%q): got %v, want errs.ErrInvalidSystemName", name, err)
		}
	}
}

// --- helpers ---

// walkNames collects every name Walk yields under prefix.

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
