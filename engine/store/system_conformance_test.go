package store_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/storefx"
)

// SystemStore behavioural contract (ADR-57), exercised against the
// engine's in-process implementation through the public API only.
//
// There is a single SystemStore implementation, so these live as
// plain tests here rather than a shared conformance package: the
// Factory indirection that internal/testutil/indextest needs (sqlite
// + a future postgres backend) buys nothing with one implementation.
// If a genuinely second backend ever lands (in-memory, network), hoist
// the bodies back into a testutil suite and add a Factory then.
//
// Walk assertions are written relative to a baseline captured right
// after init: a freshly initialised store already carries a bootstrap
// system/config version, so the invariant under test is the delta a
// single operation produces, not an absolute count or set. System
// artifacts are never indexed (ADR-85), so the index assertion just
// checks that a Put leaves the index unchanged.

// newSystemStore builds a fresh Plain store and returns its System()
// facade plus the same StoreIndex instance (via the Reopener) so
// tests can assert on index routing. The store is closed on cleanup;
// the driver and index register their own t.Cleanup inside storefx.
func newSystemStore(t *testing.T) (store.SystemStore, index.StoreIndex) {
	t.Helper()
	s, r := storefx.InitPlain(t)
	t.Cleanup(func() { _ = s.Close() })
	return s.System(), r.Index()
}

func TestSystemStore_PutGetRoundTrip(t *testing.T) {
	ss, _ := newSystemStore(t)
	ctx := context.Background()
	body := []byte("hello cursor 2026-04-01T12:00:00Z")

	if err := ss.Put(ctx, store.SystemArtifact{Name: "scrub/cursor", Payload: bytes.NewReader(body)}); err != nil {
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
	ss, _ := newSystemStore(t)
	ctx := context.Background()
	v1 := []byte("version-1")
	v2 := []byte("version-2-newer")

	if err := ss.Put(ctx, store.SystemArtifact{Name: "scrub/cursor", Payload: bytes.NewReader(v1)}); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := ss.Put(ctx, store.SystemArtifact{Name: "scrub/cursor", Payload: bytes.NewReader(v2)}); err != nil {
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
	ss, _ := newSystemStore(t)
	_, err := ss.Get(context.Background(), "gc/cursor-never-written")
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("Get absent: got %v, want errs.ErrArtifactNotFound", err)
	}
}

func TestSystemStore_DeleteIdempotent(t *testing.T) {
	ss, _ := newSystemStore(t)
	ctx := context.Background()

	// Delete on absent — no-op.
	if err := ss.Delete(ctx, "scrub/cursor"); err != nil {
		t.Errorf("Delete absent: got %v, want nil", err)
	}

	// Put then Delete then Get returns NotFound.
	if err := ss.Put(ctx, store.SystemArtifact{Name: "scrub/cursor", Payload: bytes.NewReader([]byte("x"))}); err != nil {
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
	ss, _ := newSystemStore(t)
	ctx := context.Background()
	for _, name := range []string{
		"scrub/cursor",
		"scrub/last-failed",
		"gc/cursor",
		"snapshot/2026-04-01",
	} {
		if err := ss.Put(ctx, store.SystemArtifact{Name: name, Payload: bytes.NewReader([]byte(name))}); err != nil {
			t.Fatalf("Put %q: %v", name, err)
		}
	}

	// scrub/ is state-only and never touched by init, so an absolute
	// assertion is safe here (unlike the empty-prefix case below).
	got := walkNames(t, ss, "scrub/")
	sort.Strings(got)
	want := []string{"scrub/cursor", "scrub/last-failed"}
	if !equalSlices(got, want) {
		t.Errorf("Walk(scrub/): got %v, want %v", got, want)
	}
}

func TestSystemStore_WalkEmptyPrefixScansAll(t *testing.T) {
	ss, _ := newSystemStore(t)
	ctx := context.Background()

	// Baseline: a freshly initialised store already exposes
	// its initial config version through Walk. Assert on the delta.
	before := walkNames(t, ss, "")

	for _, n := range []string{"config/v1", "scrub/cursor", "gc/cursor"} {
		if err := ss.Put(ctx, store.SystemArtifact{Name: n, Payload: bytes.NewReader([]byte(n))}); err != nil {
			t.Fatalf("Put %q: %v", n, err)
		}
	}

	got := setDiff(walkNames(t, ss, ""), before)
	sort.Strings(got)
	want := []string{"config/v1", "gc/cursor", "scrub/cursor"}
	if !equalSlices(got, want) {
		t.Errorf(`Walk("") delta: got %v, want %v`, got, want)
	}
}

func TestSystemStore_PutDoesNotIndex(t *testing.T) {
	ss, idx := newSystemStore(t)
	ctx := context.Background()
	body := []byte("snapshot-payload-1234")

	before := countNamespace(t, idx, domain.NamespaceWildcard)

	if err := ss.Put(ctx, store.SystemArtifact{Name: "index_checkpoint/2026-04-01", Payload: bytes.NewReader(body)}); err != nil {
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
	if d := countNamespace(t, idx, domain.NamespaceWildcard) - before; d != 0 {
		t.Errorf("system artifact appeared in index (delta %d, want 0)", d)
	}
}

func TestSystemStore_InvalidNamesRejected(t *testing.T) {
	ss, _ := newSystemStore(t)
	ctx := context.Background()
	for _, name := range []string{
		"",
		"/leading",
		"trailing/",
		"double//slash",
		"a/./b",
		"a/../b",
	} {
		err := ss.Put(ctx, store.SystemArtifact{Name: name, Payload: bytes.NewReader([]byte("x"))})
		if !errors.Is(err, errs.ErrInvalidSystemName) {
			t.Errorf("Put(%q): got %v, want errs.ErrInvalidSystemName", name, err)
		}
	}
}

// --- helpers ---

// walkNames collects every name Walk yields under prefix.
func walkNames(t *testing.T, ss store.SystemStore, prefix string) []string {
	t.Helper()
	var names []string
	err := ss.Walk(context.Background(), prefix, func(name string, _ domain.Manifest) error {
		names = append(names, name)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk(%q): %v", prefix, err)
	}
	return names
}

// countNamespace returns the number of index rows in ns.
func countNamespace(t *testing.T, idx index.StoreIndex, ns string) int {
	t.Helper()
	var n int
	err := idx.ListByNamespace(context.Background(), ns, func(_ domain.Manifest) error {
		n++
		return nil
	})
	if err != nil {
		t.Fatalf("ListByNamespace(%q): %v", ns, err)
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

// equalSlices reports whether a and b hold the same elements in order.
func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
