package core_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/testutil/driverfx"
	"github.com/rkurbatov/scrinium/internal/testutil/indexfx"
	"github.com/rkurbatov/scrinium/internal/testutil/storefx"
)

// --- InitStore happy path ---

func TestInitStore_FreshLocation_Succeeds(t *testing.T) {
	drv := driverfx.LocalFS(t)

	s, kit, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if s == nil {
		t.Fatal("nil Store returned")
	}
	if kit != nil {
		t.Errorf("expected nil RecoveryKit on Plain Store, got %d bytes", len(kit))
	}
	if s.State() != domain.StateUnlocked {
		t.Errorf("state: got %v, want %v", s.State(), domain.StateUnlocked)
	}

	desc, err := descriptor.Read(context.Background(), drv)
	if err != nil {
		t.Fatalf("read descriptor: %v", err)
	}
	if desc.StoreID == "" {
		t.Error("descriptor.StoreID is empty")
	}
	if desc.SchemaVersion != descriptor.CurrentSchemaVersion {
		t.Errorf("descriptor.SchemaVersion: got %d, want %d",
			desc.SchemaVersion, descriptor.CurrentSchemaVersion)
	}
	if desc.Sequence != 1 {
		t.Errorf("descriptor.Sequence on InitStore: got %d, want 1", desc.Sequence)
	}
	if desc.DEKEncrypted {
		t.Error("descriptor.DEKEncrypted should be false on Plain Store")
	}

	// Projection params now live in system.config — read them via
	// the active config snapshot.
	cfg := s.Config()
	if cfg.ManifestCrypto != "Plain" {
		t.Errorf("default ManifestCrypto: got %q, want Plain", cfg.ManifestCrypto)
	}
	if cfg.PathTopology != "Sharded" {
		t.Errorf("default PathTopology: got %q, want Sharded", cfg.PathTopology)
	}
	if cfg.ContentHasher != "sha256" {
		t.Errorf("default ContentHasher: got %q, want sha256", cfg.ContentHasher)
	}

	// Capabilities pass-through.
	caps := s.Capabilities()
	if caps == 0 {
		t.Error("Capabilities returned zero — driver should expose flags")
	}
}

func TestInitStore_NilDriver(t *testing.T) {
	_, _, err := core.InitStore(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on nil driver")
	}
}

// TestInitStore_RequiresStoreIndex verifies the dependency
// inversion: core does not auto-build an index, the caller must
// provide one.
func TestInitStore_RequiresStoreIndex(t *testing.T) {
	drv := driverfx.LocalFS(t)
	_, _, err := core.InitStore(context.Background(), drv)
	if err == nil {
		t.Fatal("expected error when WithStoreIndex is not provided")
	}
	if !strings.Contains(err.Error(), "WithStoreIndex") {
		t.Errorf("error should mention missing WithStoreIndex: %v", err)
	}
}

// --- ExistingStore guards ---

func TestInitStore_AlreadyExists(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatalf("first InitStore: %v", err)
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrStoreAlreadyExists) {
		t.Fatalf("expected errs.ErrStoreAlreadyExists, got %v", err)
	}
}

func TestInitStore_ForceReinit(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatalf("first InitStore: %v", err)
	}
	desc1, _ := descriptor.Read(context.Background(), drv)
	id1 := desc1.StoreID

	s2, _, err := core.InitStore(context.Background(), drv,
		core.WithForceReinit(),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("force-reinit InitStore: %v", err)
	}
	if s2.State() != domain.StateUnlocked {
		t.Errorf("state after force reinit: got %v, want %v", s2.State(), domain.StateUnlocked)
	}
	desc2, _ := descriptor.Read(context.Background(), drv)
	if desc2.StoreID == id1 {
		t.Errorf("force reinit kept the same StoreID %q — should have generated a new one", id1)
	}
}

func TestInitStore_CorruptedDescriptor_NoForce(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if err := drv.Put(context.Background(), descriptor.Path,
		strings.NewReader(`{not json`)); err != nil {
		t.Fatal(err)
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrStoreCorrupted) {
		t.Fatalf("expected errs.ErrStoreCorrupted, got %v", err)
	}
}

func TestInitStore_CorruptedDescriptor_WithForce(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if err := drv.Put(context.Background(), descriptor.Path,
		strings.NewReader(`{not json`)); err != nil {
		t.Fatal(err)
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithForceReinit(),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("WithForceReinit must clobber corrupted descriptor: %v", err)
	}
}

// --- Custom config / immutable validation ---

func TestInitStore_CustomConfigPersisted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashBLAKE3,
		ManifestEncoding: domain.ManifestEncodingBinary,
	}
	s, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	got := s.Config()
	if got.PathTopology != "Flat" {
		t.Errorf("PathTopology: got %q, want Flat", got.PathTopology)
	}
	if got.ContentHasher != "blake3" {
		t.Errorf("ContentHasher: got %q, want blake3", got.ContentHasher)
	}
	if got.ManifestEncoding != "Binary" {
		t.Errorf("ManifestEncoding: got %q, want Binary", got.ManifestEncoding)
	}
}

func TestInitStore_RejectsInvalidConfig(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{ContentHasher: "md5"}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("expected errs.ErrInvalidConfig, got %v", err)
	}
}

func TestInitStore_NativeTopologyRequiresExternalRef(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{
		PathTopology: domain.PathTopologyNative,
		BlobStorage:  domain.BlobStorageTarget,
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("expected errs.ErrInvalidConfig, got %v", err)
	}
}

// TestInitStore_DiskBackedIndex demonstrates the on-disk wiring
// pattern documented in DI Example: the caller chooses where the
// index lives (here, under HostStorage at a fixed path); core has
// no opinion on the location.
func TestInitStore_DiskBackedIndex(t *testing.T) {
	drv := driverfx.LocalFS(t)
	host := t.TempDir()
	idxPath := filepath.Join(host, "myindex.db")
	idx := indexfx.Disk(t, idxPath)

	s, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if s.State() != domain.StateUnlocked {
		t.Errorf("state: got %v, want %v", s.State(), domain.StateUnlocked)
	}
	if _, err := os.Stat(idxPath); err != nil {
		t.Errorf("disk-backed index file: %v", err)
	}
	// core must NOT have created a default system.index/index.db
	// inside the Driver root — that path is no longer hard-coded.
	defaultPath := filepath.Join(drv.Root(), "system.index", "index.db")
	if _, err := os.Stat(defaultPath); err == nil {
		t.Errorf("core unexpectedly created %s", defaultPath)
	}
}

// --- StoreID uniqueness ---

func TestInitStore_GeneratesUniqueStoreIDs(t *testing.T) {
	const N = 5
	ids := make(map[string]bool, N)
	for i := 0; i < N; i++ {
		drv := driverfx.LocalFS(t)
		_, _, err := core.InitStore(context.Background(), drv,
			core.WithStoreIndex(indexfx.Memory(t)),
			core.WithHashRegistry(storefx.Hashes()),
		)
		if err != nil {
			t.Fatal(err)
		}
		desc, _ := descriptor.Read(context.Background(), drv)
		if ids[desc.StoreID] {
			t.Fatalf("duplicate StoreID %q", desc.StoreID)
		}
		ids[desc.StoreID] = true
	}
}

// --- OpenStore: error paths ---

func TestOpenStore_NilDriver(t *testing.T) {
	_, err := core.OpenStore(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on nil driver")
	}
}

func TestOpenStore_FreshLocation_NotFound(t *testing.T) {
	drv := driverfx.LocalFS(t)
	_, err := core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrStoreNotFound) {
		t.Fatalf("expected errs.ErrStoreNotFound, got %v", err)
	}
}

func TestOpenStore_CorruptedDescriptor(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if err := drv.Put(context.Background(), descriptor.Path,
		strings.NewReader(`{not json`)); err != nil {
		t.Fatal(err)
	}
	_, err := core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrStoreCorrupted) {
		t.Fatalf("expected errs.ErrStoreCorrupted, got %v", err)
	}
}

func TestOpenStore_RequiresStoreIndex(t *testing.T) {
	drv := driverfx.LocalFS(t)
	// Init first so the descriptor is in place.
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	_, err := core.OpenStore(context.Background(), drv)
	if err == nil {
		t.Fatal("expected error when WithStoreIndex is not provided")
	}
	if !strings.Contains(err.Error(), "WithStoreIndex") {
		t.Errorf("error should mention missing WithStoreIndex: %v", err)
	}
}

// --- OpenStore: happy paths ---

func TestOpenStore_NoConfig_Succeeds(t *testing.T) {
	drv := driverfx.LocalFS(t)

	// Init with custom config so we can assert it is restored
	// faithfully on Open.
	custom := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashBLAKE3,
		ManifestEncoding: domain.ManifestEncodingBinary,
	}
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(custom),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	// Open without WithConfig — legitimate diagnostic-style open.
	s, err := core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if s.State() != domain.StateUnlocked {
		t.Errorf("state: got %v, want %v", s.State(), domain.StateUnlocked)
	}
}

func TestOpenStore_MatchingConfig_Succeeds(t *testing.T) {
	drv := driverfx.LocalFS(t)
	custom := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashBLAKE3,
		ManifestEncoding: domain.ManifestEncodingBinary,
	}
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(custom),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	// Re-open with the SAME immutable values — must succeed.
	s, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(custom),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if s.State() != domain.StateUnlocked {
		t.Errorf("state: got %v, want %v", s.State(), domain.StateUnlocked)
	}
}

// --- OpenStore: errs.ErrConfigMismatch ---

func TestOpenStore_ConfigMismatch_PathTopology(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{PathTopology: domain.PathTopologyFlat}),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	// Reopen with conflicting immutable.
	_, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{PathTopology: domain.PathTopologySharded}),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrConfigMismatch) {
		t.Fatalf("expected errs.ErrConfigMismatch, got %v", err)
	}
}

func TestOpenStore_ConfigMismatch_ContentHasher(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{ContentHasher: domain.HashSHA256}),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	_, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{ContentHasher: domain.HashBLAKE3}),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrConfigMismatch) {
		t.Fatalf("expected errs.ErrConfigMismatch, got %v", err)
	}
}

// TestOpenStore_PartialConfig_NoMismatchOnUnsetFields verifies that
// an empty/zero immutable field in WithConfig does not trigger
// errs.ErrConfigMismatch — it is treated as "not asserted by the caller"
// rather than as a request for the zero value.
func TestOpenStore_PartialConfig_NoMismatchOnUnsetFields(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{
			PathTopology:  domain.PathTopologyFlat,
			ContentHasher: domain.HashBLAKE3,
		}),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	// Reopen with only one immutable specified — the other ones
	// stay zero and must NOT be compared.
	_, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{
			ContentHasher: domain.HashBLAKE3,
			// PathTopology omitted — must not be checked.
		}),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Errorf("partial WithConfig should not trigger mismatch, got %v", err)
	}
}

// TestOpenStore_DeletionPolicyLock_OnlyChecksWhenSet verifies the
// asymmetric semantics: the caller can ask to confirm the lock is
// engaged (mismatch if it is not), but cannot ask for "no lock" by
// passing false (false is the zero value, indistinguishable from
// "not asserted").
func TestOpenStore_DeletionPolicyLock_OnlyChecksWhenSet(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{DeletionPolicyLock: false}),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	// Caller asks to confirm the lock is engaged — but the
	// descriptor says it is not. This MUST mismatch.
	_, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{DeletionPolicyLock: true}),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrConfigMismatch) {
		t.Fatalf("expected errs.ErrConfigMismatch on stricter lock request, got %v", err)
	}

	// Caller does NOT assert anything (false). MUST succeed.
	_, err = core.OpenStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{DeletionPolicyLock: false}),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Errorf("opening with relaxed lock request must succeed, got %v", err)
	}
}

// --- OpenStore: encrypted Store rejection in M1.4 ---

func TestOpenStore_EncryptedStoreNotYetSupported(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)

	// Init normally — system.config will hold Plain.
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	// Overwrite system.config with a MetadataOnly variant. The
	// pointer is bumped to the new ArtifactID atomically.
	// WriteSystemConfig is a test-only export of the package-private
	// writer (see core/export_test.go).
	bad := domain.StoreConfig{
		PathTopology:     domain.PathTopologySharded,
		ContentHasher:    domain.HashSHA256,
		ManifestEncoding: domain.ManifestEncodingJSON,
		ManifestStorage:  domain.ManifestStorageLocal,
		ManifestCrypto:   domain.ManifestCryptoMetadataOnly,
	}
	if _, err := core.WriteSystemConfig(context.Background(), drv, idx, storefx.Hashes(), bad); err != nil {
		t.Fatalf("WriteSystemConfig: %v", err)
	}

	_, err := core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err == nil {
		t.Fatal("expected encrypted-Store rejection in M1.4")
	}
	if !strings.Contains(err.Error(), "M2") {
		t.Errorf("error should reference M2 milestone: %v", err)
	}
}

// TestOpenStore_RestoresImmutableConfigFromSystemConfig verifies that
// the active StoreConfig of the opened Store reflects the values
// persisted in system.config, not whatever defaults the caller
// might pass through.
func TestOpenStore_RestoresImmutableConfigFromSystemConfig(t *testing.T) {
	drv := driverfx.LocalFS(t)
	custom := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashBLAKE3,
		ManifestEncoding: domain.ManifestEncodingBinary,
	}
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(custom),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	s, err := core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	got := s.Config()
	if got.PathTopology != "Flat" || got.ContentHasher != "blake3" {
		t.Errorf("active config should preserve InitStore values: %+v", got)
	}
}
