package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver/localfs"
	sqliteindex "scrinium.dev/engine/index/sqlite"
	"scrinium.dev/engine/internal/namedartifact"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/storefx"
)

// TestStore_FullLifecycle_DiskBacked is the end-to-end smoke test
// for the full Store lifecycle. It exercises every public-facing
// transition through real on-disk artifacts:
//
//  1. InitStore creates store.json (descriptor, §10.1.3), the first
//     system/config version (the inline config manifest, ADR-85), and a
//     SQLite index file on a localfs-backed driver.
//  2. Capacity, Walk, SetMaintenanceMode work on the open Store.
//  3. The caller closes the StoreIndex (DI ownership).
//  4. A fresh session reopens the Location: same StoreID, same
//     active config — descriptor and system.config both survived.
//  5. errs.ErrConfigMismatch on a reopen with a conflicting
//     immutable parameter.
func TestStore_FullLifecycle_DiskBacked(t *testing.T) {
	ctx := t.Context()
	location := t.TempDir()
	indexPath := filepath.Join(t.TempDir(), "index.db")

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
		PathTopology:  domain.PathTopologyFlat,
		ContentHasher: domain.HashBLAKE3,
	}
	s1, kit, err := store.InitStore(context.Background(), drv1,
		store.WithConfig(custom),
		store.WithStoreIndex(idx1),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if kit != nil {
		t.Errorf("RecoveryKit on Plain Store should be nil, got %d bytes", len(kit))
	}
	if s1.State() != domain.StateUnlocked {
		t.Errorf("phase 1 state: got %v, want %v", s1.State(), domain.StateUnlocked)
	}

	// On-disk artefacts.
	descPath := filepath.Join(location, descriptor.Path)
	if _, err := os.Stat(descPath); err != nil {
		t.Fatalf("descriptor not on disk after Init: %v", err)
	}
	cfgVersion, err := namedartifact.VersionPath("store.config", 1)
	if err != nil {
		t.Fatalf("VersionPath: %v", err)
	}
	if _, err := os.Stat(filepath.Join(location, filepath.FromSlash(cfgVersion))); err != nil {
		t.Fatalf("system config version not on disk after Init: %v", err)
	}
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("index not on disk after Init: %v", err)
	}

	desc1, err := descriptor.Read(context.Background(), drv1)
	if err != nil {
		t.Fatalf("read descriptor: %v", err)
	}
	if desc1.StoreID == "" {
		t.Fatal("StoreID is empty in descriptor")
	}
	if desc1.SchemaVersion != descriptor.CurrentSchemaVersion {
		t.Errorf("SchemaVersion: got %d, want %d", desc1.SchemaVersion, descriptor.CurrentSchemaVersion)
	}
	if desc1.Sequence != 1 {
		t.Errorf("Sequence: got %d, want 1", desc1.Sequence)
	}

	cfg1 := s1.Config()
	if cfg1.PathTopology != "Flat" {
		t.Errorf("active PathTopology: got %q, want Flat", cfg1.PathTopology)
	}
	if cfg1.ContentHasher != "blake3" {
		t.Errorf("active ContentHasher: got %q, want blake3", cfg1.ContentHasher)
	}

	// --- Phase 2: exercise the open Store ---

	info, err := s1.Capacity(context.Background())
	if err != nil {
		t.Fatalf("Capacity (phase 2): %v", err)
	}
	if info.ArtifactCount != 0 || info.BlobCount != 0 {
		// system.config artifact lives in a reserved namespace and
		// is excluded from user-visible Capacity counters; still
		// asserting empty here is safe.
		t.Errorf("Capacity on fresh Store: %+v", info)
	}

	var walked int
	if err := s1.Walk(context.Background(), func(m domain.Manifest) error {
		walked++
		return nil
	}); err != nil {
		t.Fatalf("Walk (phase 2): %v", err)
	}
	if walked != 0 {
		t.Errorf("Walk on fresh Store yielded %d manifests", walked)
	}

	if err := s1.SetMaintenanceMode(context.Background(), domain.MaintenanceModeReadOnly); err != nil {
		t.Errorf("SetMaintenanceMode ReadOnly: %v", err)
	}
	if err := s1.SetMaintenanceMode(context.Background(), domain.MaintenanceModeNone); err != nil {
		t.Errorf("SetMaintenanceMode None: %v", err)
	}

	// --- Phase 3: write some index content directly, then close ---
	addr := domain.PhysicalAddress{
		Path: "blobs/aa/bb/blob-test",
	}
	manifest := domain.Manifest{
		ArtifactID:   "art-test",
		ContentHash:  "sha256-" + domain.ContentHash("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"),
		BlobRefs:     []domain.BlobRef{"blob-test"},
		OriginalSize: 1024,
	}
	if err := idx1.IndexManifest(ctx, manifest, addr); err != nil {
		t.Fatalf("seed index: %v", err)
	}
	if err := idx1.Close(); err != nil {
		t.Fatalf("close index (phase 3): %v", err)
	}

	// --- Phase 4: reopen ---
	drv2, err := localfs.New(location, localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New (phase 4): %v", err)
	}
	idx2, err := sqliteindex.NewStore(context.Background(), indexPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore (phase 4): %v", err)
	}

	s2, err := store.OpenStore(context.Background(), drv2,
		store.WithStoreIndex(idx2),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if s2.State() != domain.StateUnlocked {
		t.Errorf("phase 4 state: got %v, want %v", s2.State(), domain.StateUnlocked)
	}

	desc2, err := descriptor.Read(context.Background(), drv2)
	if err != nil {
		t.Fatalf("read descriptor (phase 4): %v", err)
	}
	if desc2.StoreID != desc1.StoreID {
		t.Errorf("StoreID changed across reopen: %q -> %q", desc1.StoreID, desc2.StoreID)
	}

	cfg2 := s2.Config()
	if cfg2.PathTopology != cfg1.PathTopology {
		t.Errorf("PathTopology changed across reopen: %q -> %q", cfg1.PathTopology, cfg2.PathTopology)
	}
	if cfg2.ContentHasher != cfg1.ContentHasher {
		t.Errorf("ContentHasher changed across reopen: %q -> %q", cfg1.ContentHasher, cfg2.ContentHasher)
	}

	gotAddr, err := idx2.Resolve(ctx, "blob-test")
	if err != nil {
		t.Fatalf("Resolve after reopen: %v", err)
	}
	if gotAddr.Path != "blobs/aa/bb/blob-test" {
		t.Errorf("blob path after reopen: got %q, want %q",
			gotAddr.Path, "blobs/aa/bb/blob-test")
	}

	if err := idx2.Close(); err != nil {
		t.Fatalf("close index (phase 4): %v", err)
	}

	// --- Phase 5: errs.ErrConfigMismatch on conflicting reopen ---
	drv3, err := localfs.New(location, localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New (phase 5): %v", err)
	}
	idx3, err := sqliteindex.NewStore(context.Background(), indexPath)
	if err != nil {
		t.Fatalf("sqlite.NewStore (phase 5): %v", err)
	}
	defer idx3.Close()

	conflict := domain.StoreConfig{
		PathTopology: domain.PathTopologySharded, // active is Flat
	}
	_, err = store.OpenStore(context.Background(), drv3,
		store.WithConfig(conflict),
		store.WithStoreIndex(idx3),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrConfigMismatch) {
		t.Fatalf("expected errs.ErrConfigMismatch on conflicting reopen, got %v", err)
	}
}
