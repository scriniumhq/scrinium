package storesuite

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"sort"
	"testing"

	"scrinium.dev/engine/systemstore"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/storefx"
	"scrinium.dev/testutil/storekit"
)

// SystemStore keep=0 (exclusive-cell) routing (ADR-100/101). The
// conformance suite above exercises the versioned path; these cover the
// cell form and the cell/versioned coexistence the contract added:
// KeepCell() writes a single fixed slot, Get probes cell after versions,
// Delete clears it, and Walk merges cells with versioned actives.
//
// Names use a "celltest/" prefix that store init never touches, so Walk
// assertions can be absolute.

func TestSystemStore_CellPutGetRoundTrip(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()
	body := []byte("lease-holder-A")

	if err := ss.Put(ctx, systemstore.Artifact{
		Name:    "celltest/lease",
		Payload: bytes.NewReader(body),
		Keep:    systemstore.KeepCell(),
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
}

func TestSystemStore_CellOverwriteReplaces(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()
	put := func(b string) {
		t.Helper()
		if err := ss.Put(ctx, systemstore.Artifact{
			Name:    "celltest/lease",
			Payload: bytes.NewReader([]byte(b)),
			Keep:    systemstore.KeepCell(),
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
}

// TestSystemStore_CellAndVersionedGetRoute: a cell and a versioned name
// coexist, and Get resolves each to its own form.
func TestSystemStore_CellAndVersionedGetRoute(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()

	if err := ss.Put(ctx, systemstore.Artifact{
		Name: "celltest/lease", Payload: bytes.NewReader([]byte("cell")), Keep: systemstore.KeepCell(),
	}); err != nil {
		t.Fatalf("Put cell: %v", err)
	}
	if err := ss.Put(ctx, systemstore.Artifact{
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

func TestSystemStore_DeleteCell(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()

	if err := ss.Put(ctx, systemstore.Artifact{
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
}

// TestSystemStore_WalkIncludesCells: Walk surfaces both versioned actives
// and keep=0 cells under a prefix (so the lease is visible in the view).
func TestSystemStore_WalkIncludesCells(t *testing.T) {
	ss, _ := storefx.InitPlainSystem(t)
	ctx := context.Background()

	if err := ss.Put(ctx, systemstore.Artifact{
		Name: "celltest/lease", Payload: bytes.NewReader([]byte("L")), Keep: systemstore.KeepCell(),
	}); err != nil {
		t.Fatalf("Put cell: %v", err)
	}
	if err := ss.Put(ctx, systemstore.Artifact{
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
