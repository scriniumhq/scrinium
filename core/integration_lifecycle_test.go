package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/driver/localfs"
	sqliteindex "github.com/rkurbatov/scrinium/index/sqlite"
)

// TestM13_FullLifecycle_DiskBacked is the end-to-end smoke test
// for milestone M1.3. It exercises the full lifecycle of a Store
// through real on-disk artifacts:
//
//  1. InitStore creates store.json and a SQLite index file on
//     a localfs-backed driver. The Store is in StateUnlocked.
//  2. Capacity, Walk, and SetMaintenanceMode work on the open
//     Store.
//  3. The caller (= the test) closes the StoreIndex when done.
//     This is the documented DI-style ownership: the index is
//     injected from the outside, so the outside must close it.
//     core itself does not close caller-owned dependencies.
//  4. A second process / session reopens the Location with a
//     fresh sqlite.NewStore over the SAME on-disk file. It also
//     observes StateUnlocked and the same StoreID — the
//     descriptor and the index both survived the round-trip.
//  5. ErrConfigMismatch is raised when the second open passes a
//     WithConfig that disagrees with the descriptor on an
//     immutable parameter.
//
// This test deliberately uses on-disk SQLite (not :memory:) so it
// exercises the realistic deployment shape and catches any
// regression in the file format, the open/close sequence, or the
// migration replay.
func TestM13_FullLifecycle_DiskBacked(t *testing.T) {
	location := t.TempDir()
	indexPath := filepath.Join(t.TempDir(), "index.db")

	// Sanity: nothing on disk before we start.
	if _, err := os.Stat(filepath.Join(location, descriptor.Path)); !os.IsNotExist(err) {
		t.Fatalf("descriptor unexpectedly present before Init: %v", err)
	}
	if _, err := os.Stat(indexPath); !os.IsNotExist(err) {
		t.Fatalf("index unexpectedly present before Init: %v", err)
	}

	// --- Phase 1: InitStore ---

	drv1, err := localfs.New(location, localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New (phase 1): %v", err)
	}
	idx1, err := sqliteindex.NewStore(context.Background(), indexPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore (phase 1): %v", err)
	}

	custom := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashBLAKE3,
		ManifestEncoding: domain.ManifestEncodingBinary,
	}
	s1, kit, err := core.InitStore(context.Background(), drv1,
		core.WithConfig(custom),
		core.WithStoreIndex(idx1),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if kit != nil {
		t.Errorf("RecoveryKit on Plain Store should be nil, got %d bytes", len(kit))
	}
	if s1.State() != core.StateUnlocked {
		t.Errorf("phase 1 state: got %v, want %v", s1.State(), core.StateUnlocked)
	}

	// On-disk artefacts are present.
	descPath := filepath.Join(location, descriptor.Path)
	if _, err := os.Stat(descPath); err != nil {
		t.Fatalf("descriptor not on disk after Init: %v", err)
	}
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("index not on disk after Init: %v", err)
	}

	// Read descriptor through the package directly, snapshot the
	// values that must survive a reopen.
	desc1, err := descriptor.Read(context.Background(), drv1)
	if err != nil {
		t.Fatalf("read descriptor: %v", err)
	}
	if desc1.StoreID == "" {
		t.Fatal("StoreID is empty in descriptor")
	}
	if desc1.PathTopology != "Flat" {
		t.Errorf("descriptor.PathTopology: got %q, want Flat", desc1.PathTopology)
	}
	if desc1.ContentHasher != "blake3" {
		t.Errorf("descriptor.ContentHasher: got %q, want blake3", desc1.ContentHasher)
	}

	// --- Phase 2: exercise the open Store ---

	// Capacity is empty.
	info, err := s1.Capacity(context.Background())
	if err != nil {
		t.Fatalf("Capacity (phase 2): %v", err)
	}
	if info.ArtifactCount != 0 || info.BlobCount != 0 {
		t.Errorf("Capacity on fresh Store: %+v", info)
	}

	// Walk is empty.
	var walked int
	if err := s1.Walk(context.Background(), "*", func(m domain.Manifest) error {
		walked++
		return nil
	}); err != nil {
		t.Fatalf("Walk (phase 2): %v", err)
	}
	if walked != 0 {
		t.Errorf("Walk on fresh Store yielded %d manifests", walked)
	}

	// Maintenance mode round-trip.
	if err := s1.SetMaintenanceMode(context.Background(), core.MaintenanceModeReadOnly); err != nil {
		t.Errorf("SetMaintenanceMode ReadOnly: %v", err)
	}
	if err := s1.SetMaintenanceMode(context.Background(), core.MaintenanceModeNone); err != nil {
		t.Errorf("SetMaintenanceMode None: %v", err)
	}

	// --- Phase 3: write some index content directly, then close ---
	//
	// We exercise the index through its own contract — Put/Get on
	// the Store are still stubs in M1.3. This step is what makes
	// Phase 4 a real persistence test: if the index does not
	// survive close/reopen, Resolve in Phase 4 will not find the
	// blob we record now.

	addr := core.PhysicalAddress{
		Workspace: core.WorkspaceLocation,
		Path:      "blobs/aa/bb/blob-test",
	}
	manifest := domain.Manifest{
		ArtifactID:   "art-test",
		Type:         domain.ManifestTypeBlob,
		Namespace:    "users",
		ContentHash:  "sha256-" + domain.ContentHash("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		BlobRef:      "blob-test",
		OriginalSize: 1024,
	}
	if err := idx1.IndexManifest(manifest, addr, nil, nil); err != nil {
		t.Fatalf("seed index: %v", err)
	}

	// Close the index — this is the caller's responsibility per
	// the DI contract.
	if err := closeIndex(idx1); err != nil {
		t.Fatalf("close index (phase 3): %v", err)
	}

	// --- Phase 4: reopen the Store in a fresh "session" ---
	//
	// New driver instance, new index instance, same on-disk files.
	// This simulates a process restart.

	drv2, err := localfs.New(location, localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New (phase 4): %v", err)
	}
	idx2, err := sqliteindex.NewStore(context.Background(), indexPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore (phase 4): %v", err)
	}

	// Open without WithConfig — descriptor is the source of truth
	// for immutable fields.
	s2, err := core.OpenStore(context.Background(), drv2,
		core.WithStoreIndex(idx2),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if s2.State() != core.StateUnlocked {
		t.Errorf("phase 4 state: got %v, want %v", s2.State(), core.StateUnlocked)
	}

	// StoreID must be identical — the descriptor was preserved.
	desc2, err := descriptor.Read(context.Background(), drv2)
	if err != nil {
		t.Fatalf("read descriptor (phase 4): %v", err)
	}
	if desc2.StoreID != desc1.StoreID {
		t.Errorf("StoreID changed across reopen: %q -> %q", desc1.StoreID, desc2.StoreID)
	}
	if desc2.PathTopology != desc1.PathTopology {
		t.Errorf("PathTopology changed across reopen: %q -> %q", desc1.PathTopology, desc2.PathTopology)
	}
	if desc2.ContentHasher != desc1.ContentHasher {
		t.Errorf("ContentHasher changed across reopen: %q -> %q", desc1.ContentHasher, desc2.ContentHasher)
	}

	// The index content must have survived the close/reopen.
	gotAddr, err := idx2.Resolve("blob-test")
	if err != nil {
		t.Fatalf("Resolve after reopen: %v", err)
	}
	if gotAddr.Path != "blobs/aa/bb/blob-test" {
		t.Errorf("blob path after reopen: got %q, want %q",
			gotAddr.Path, "blobs/aa/bb/blob-test")
	}

	// LastWrittenAt advanced (descriptor was rewritten neither by
	// nor since OpenStore in M1.3 — that lands in M2 alongside
	// auto-heal). For now we only assert it is non-empty.
	if desc2.LastWrittenAt == "" {
		t.Error("LastWrittenAt is empty in reopened descriptor")
	}

	// Clean up the second-session index.
	if err := closeIndex(idx2); err != nil {
		t.Fatalf("close index (phase 4): %v", err)
	}

	// --- Phase 5: ErrConfigMismatch on conflicting reopen ---
	//
	// Open the same Location yet again, but this time pass a
	// WithConfig that disagrees with the descriptor on an
	// immutable. Must fail with ErrConfigMismatch — the validator
	// is what protects callers from accidentally opening someone
	// else's Store as if it were theirs.

	drv3, err := localfs.New(location, localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New (phase 5): %v", err)
	}
	idx3, err := sqliteindex.NewStore(context.Background(), indexPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore (phase 5): %v", err)
	}
	defer closeIndex(idx3)

	conflict := domain.StoreConfig{
		PathTopology: domain.PathTopologySharded, // descriptor has Flat
	}
	_, err = core.OpenStore(context.Background(), drv3,
		core.WithConfig(conflict),
		core.WithStoreIndex(idx3),
	)
	if !errors.Is(err, core.ErrConfigMismatch) {
		t.Fatalf("expected ErrConfigMismatch on conflicting reopen, got %v", err)
	}
}

// closeIndex is the test-side ergonomics for releasing a
// caller-owned StoreIndex. The interface does not declare Close,
// but every concrete implementation (sqlite, postgres, in-memory)
// has it; the type assertion is the documented escape hatch.
//
// Returns nil if the implementation does not expose a Close, so a
// test can call it unconditionally without branching.
func closeIndex(idx core.StoreIndex) error {
	c, ok := idx.(interface{ Close() error })
	if !ok {
		return nil
	}
	return c.Close()
}
