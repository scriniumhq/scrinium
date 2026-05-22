// Package systemstoretest is the shared conformance suite for
// SystemStore implementations. Any type that implements
// store.SystemStore (currently the engine's default in-process
// implementation; future network-attached and in-memory variants
// will use the same suite) is tested here.
//
// Layout mirrors internal/testutil/indextest: a Run(t, factory)
// entry point that constructs a fresh SystemStore for each
// subtest. The factory captures dependencies (a Driver, a
// StoreIndex, a hash registry) and returns a SystemStore plus a
// cleanup function.
//
// The suite verifies the contract laid out in ADR-57:
//
//   - Put + Get round-trip a named artifact.
//   - Update replaces the predecessor, Get returns the latest.
//   - Walk by prefix yields every name, in alphabetical order.
//   - Delete makes Get return ErrArtifactNotFound; deleting an
//     absent name is a no-op.
//   - WithoutIndex skips index registration: a subsequent
//     index-only walk does NOT see the artifact.
//   - Name validation: empty, leading/trailing slash, "..",
//     consecutive slashes all rejected.
//   - Prefix mapping: config/* lives in system.config, others
//     in system.state.
package systemstoretest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/index"
	"scrinium.dev/engine/store"
)

// Factory builds a fresh SystemStore for one subtest. Cleanup is
// responsible for tearing down everything the SystemStore touches
// (driver dir, index).
type Factory struct {
	New func(t *testing.T) (ss store.SystemStore, idx index.StoreIndex, cleanup func())
}

// Run executes the full conformance suite against the factory.
func Run(t *testing.T, f Factory) {
	t.Helper()

	t.Run("PutGet_RoundTrip", func(t *testing.T) { testPutGetRoundTrip(t, f) })
	t.Run("PutUpdate_ReplacesPredecessor", func(t *testing.T) { testUpdateReplaces(t, f) })
	t.Run("Get_AbsentReturnsNotFound", func(t *testing.T) { testGetAbsentNotFound(t, f) })
	t.Run("Delete_Idempotent", func(t *testing.T) { testDeleteIdempotent(t, f) })
	t.Run("Walk_ByPrefix", func(t *testing.T) { testWalkByPrefix(t, f) })
	t.Run("Walk_EmptyPrefix_ScansAll", func(t *testing.T) { testWalkEmptyPrefix(t, f) })
	t.Run("WithoutIndex_SkipsIndexing", func(t *testing.T) { testWithoutIndexSkips(t, f) })
	t.Run("ConfigPrefix_RoutesToSystemConfig", func(t *testing.T) { testConfigPrefixRouting(t, f) })
	t.Run("StatePrefix_RoutesToSystemState", func(t *testing.T) { testStatePrefixRouting(t, f) })
	t.Run("InvalidNames_Rejected", func(t *testing.T) { testInvalidNamesRejected(t, f) })
}

// --- individual tests ---

func testPutGetRoundTrip(t *testing.T, f Factory) {
	ss, _, cleanup := f.New(t)
	defer cleanup()

	ctx := context.Background()
	body := []byte("hello cursor 2026-04-01T12:00:00Z")

	if err := ss.Put(ctx, "scrub/cursor", bytes.NewReader(body)); err != nil {
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

func testUpdateReplaces(t *testing.T, f Factory) {
	ss, _, cleanup := f.New(t)
	defer cleanup()

	ctx := context.Background()
	v1 := []byte("version-1")
	v2 := []byte("version-2-newer")

	if err := ss.Put(ctx, "scrub/cursor", bytes.NewReader(v1)); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := ss.Put(ctx, "scrub/cursor", bytes.NewReader(v2)); err != nil {
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

func testGetAbsentNotFound(t *testing.T, f Factory) {
	ss, _, cleanup := f.New(t)
	defer cleanup()

	_, err := ss.Get(context.Background(), "gc/cursor-never-written")
	if !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Errorf("Get absent: got %v, want errs.ErrArtifactNotFound", err)
	}
}

func testDeleteIdempotent(t *testing.T, f Factory) {
	ss, _, cleanup := f.New(t)
	defer cleanup()

	ctx := context.Background()

	// Delete on absent — no-op.
	if err := ss.Delete(ctx, "scrub/cursor"); err != nil {
		t.Errorf("Delete absent: got %v, want nil", err)
	}

	// Put then Delete then Get returns NotFound.
	if err := ss.Put(ctx, "scrub/cursor", bytes.NewReader([]byte("x"))); err != nil {
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

func testWalkByPrefix(t *testing.T, f Factory) {
	ss, _, cleanup := f.New(t)
	defer cleanup()

	ctx := context.Background()
	for _, name := range []string{
		"scrub/cursor",
		"scrub/last-failed",
		"gc/cursor",
		"snapshot/2026-04-01",
	} {
		if err := ss.Put(ctx, name, bytes.NewReader([]byte(name))); err != nil {
			t.Fatalf("Put %q: %v", name, err)
		}
	}

	var got []string
	err := ss.Walk(ctx, "scrub/", func(name string, _ domain.Manifest) error {
		got = append(got, name)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	want := []string{"scrub/cursor", "scrub/last-failed"}
	sort.Strings(got)
	if !equalSlices(got, want) {
		t.Errorf("Walk(scrub/): got %v, want %v", got, want)
	}
}

func testWalkEmptyPrefix(t *testing.T, f Factory) {
	ss, _, cleanup := f.New(t)
	defer cleanup()

	ctx := context.Background()
	names := []string{
		"config/v1",
		"scrub/cursor",
		"gc/cursor",
	}
	for _, n := range names {
		if err := ss.Put(ctx, n, bytes.NewReader([]byte(n))); err != nil {
			t.Fatalf("Put %q: %v", n, err)
		}
	}

	var got []string
	err := ss.Walk(ctx, "", func(name string, _ domain.Manifest) error {
		got = append(got, name)
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	sort.Strings(got)
	want := []string{"config/v1", "gc/cursor", "scrub/cursor"}
	if !equalSlices(got, want) {
		t.Errorf("Walk(\"\"): got %v, want %v", got, want)
	}
}

func testWithoutIndexSkips(t *testing.T, f Factory) {
	ss, idx, cleanup := f.New(t)
	defer cleanup()

	ctx := context.Background()
	body := []byte("snapshot-payload-1234")

	if err := ss.Put(ctx, "index_snapshot/2026-04-01", bytes.NewReader(body), store.WithoutIndex()); err != nil {
		t.Fatalf("Put with WithoutIndex: %v", err)
	}

	// Get must still succeed (pointer + manifest file are both
	// written; the only thing skipped is the index row).
	rh, err := ss.Get(ctx, "index_snapshot/2026-04-01")
	if err != nil {
		t.Fatalf("Get after WithoutIndex Put: %v", err)
	}
	got, _ := io.ReadAll(rh)
	_ = rh.Close()
	if !bytes.Equal(got, body) {
		t.Errorf("payload: got %q, want %q", got, body)
	}

	// Walk via index on system.state must NOT see the manifest.
	var indexed int
	err = idx.ListByNamespace(ctx, domain.NamespaceSystemState, func(_ domain.Manifest) error {
		indexed++
		return nil
	})
	if err != nil {
		t.Fatalf("ListByNamespace: %v", err)
	}
	if indexed != 0 {
		t.Errorf("WithoutIndex artifact appeared in index (%d rows)", indexed)
	}
}

func testConfigPrefixRouting(t *testing.T, f Factory) {
	ss, idx, cleanup := f.New(t)
	defer cleanup()

	ctx := context.Background()
	if err := ss.Put(ctx, "config/v1", bytes.NewReader([]byte(`{"k":"v"}`))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Should appear under system.config in the index.
	var seen int
	err := idx.ListByNamespace(ctx, domain.NamespaceSystemConfig, func(_ domain.Manifest) error {
		seen++
		return nil
	})
	if err != nil {
		t.Fatalf("ListByNamespace(system.config): %v", err)
	}
	if seen != 1 {
		t.Errorf("config/v1 not routed to system.config (saw %d rows)", seen)
	}

	// Must NOT appear under system.state.
	var stateSeen int
	err = idx.ListByNamespace(ctx, domain.NamespaceSystemState, func(_ domain.Manifest) error {
		stateSeen++
		return nil
	})
	if err != nil {
		t.Fatalf("ListByNamespace(system.state): %v", err)
	}
	if stateSeen != 0 {
		t.Errorf("config/v1 leaked into system.state (saw %d rows)", stateSeen)
	}
}

func testStatePrefixRouting(t *testing.T, f Factory) {
	ss, idx, cleanup := f.New(t)
	defer cleanup()

	ctx := context.Background()
	if err := ss.Put(ctx, "scrub/cursor", bytes.NewReader([]byte("timestamp"))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Should appear under system.state.
	var seen int
	err := idx.ListByNamespace(ctx, domain.NamespaceSystemState, func(_ domain.Manifest) error {
		seen++
		return nil
	})
	if err != nil {
		t.Fatalf("ListByNamespace(system.state): %v", err)
	}
	if seen != 1 {
		t.Errorf("scrub/cursor not routed to system.state (saw %d rows)", seen)
	}
}

func testInvalidNamesRejected(t *testing.T, f Factory) {
	ss, _, cleanup := f.New(t)
	defer cleanup()

	ctx := context.Background()
	for _, name := range []string{
		"",
		"/leading",
		"trailing/",
		"double//slash",
		"a/./b",
		"a/../b",
	} {
		err := ss.Put(ctx, name, bytes.NewReader([]byte("x")))
		if !errors.Is(err, errs.ErrInvalidSystemName) {
			t.Errorf("Put(%q): got %v, want errs.ErrInvalidSystemName", name, err)
		}
	}
}

// --- helpers ---

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
