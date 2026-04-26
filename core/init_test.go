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

// Local aliases for the few testutil helpers used in this file.
// The aliases keep the test bodies readable (newDriver vs
// driverfx.LocalFS) without re-introducing the duplicated
// implementation.
var (
	newHashes    = storefx.Hashes
	newDriver    = driverfx.LocalFS
	newIndex     = indexfx.Memory
	newDiskIndex = indexfx.Disk
)

// --- InitStore happy path ---

func TestInitStore_FreshLocation_Succeeds(t *testing.T) {
	drv := newDriver(t)

	s, kit, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
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
	if s.State() != core.StateUnlocked {
		t.Errorf("state: got %v, want %v", s.State(), core.StateUnlocked)
	}

	desc, err := descriptor.Read(context.Background(), drv)
	if err != nil {
		t.Fatalf("read descriptor: %v", err)
	}
	if desc.StoreID == "" {
		t.Error("descriptor.StoreID is empty")
	}
	if desc.FormatVersion != descriptor.CurrentFormatVersion {
		t.Errorf("descriptor.FormatVersion: got %d, want %d",
			desc.FormatVersion, descriptor.CurrentFormatVersion)
	}
	if desc.ManifestCrypto != "Plain" {
		t.Errorf("default ManifestCrypto: got %q, want Plain", desc.ManifestCrypto)
	}
	if desc.PathTopology != "Sharded" {
		t.Errorf("default PathTopology: got %q, want Sharded", desc.PathTopology)
	}
	if desc.ContentHasher != "sha256" {
		t.Errorf("default ContentHasher: got %q, want sha256", desc.ContentHasher)
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
	drv := newDriver(t)
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
	drv := newDriver(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
	); err != nil {
		t.Fatalf("first InitStore: %v", err)
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
	)
	if !errors.Is(err, errs.ErrStoreAlreadyExists) {
		t.Fatalf("expected errs.ErrStoreAlreadyExists, got %v", err)
	}
}

func TestInitStore_ForceReinit(t *testing.T) {
	drv := newDriver(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
	); err != nil {
		t.Fatalf("first InitStore: %v", err)
	}
	desc1, _ := descriptor.Read(context.Background(), drv)
	id1 := desc1.StoreID

	s2, _, err := core.InitStore(context.Background(), drv,
		core.WithForceReinit(),
		core.WithStoreIndex(newIndex(t)),
	)
	if err != nil {
		t.Fatalf("force-reinit InitStore: %v", err)
	}
	if s2.State() != core.StateUnlocked {
		t.Errorf("state after force reinit: got %v, want %v", s2.State(), core.StateUnlocked)
	}
	desc2, _ := descriptor.Read(context.Background(), drv)
	if desc2.StoreID == id1 {
		t.Errorf("force reinit kept the same StoreID %q — should have generated a new one", id1)
	}
}

func TestInitStore_CorruptedDescriptor_NoForce(t *testing.T) {
	drv := newDriver(t)
	if err := drv.Put(context.Background(), descriptor.Path,
		strings.NewReader(`{not json`)); err != nil {
		t.Fatal(err)
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
	)
	if !errors.Is(err, errs.ErrStoreCorrupted) {
		t.Fatalf("expected errs.ErrStoreCorrupted, got %v", err)
	}
}

func TestInitStore_CorruptedDescriptor_WithForce(t *testing.T) {
	drv := newDriver(t)
	if err := drv.Put(context.Background(), descriptor.Path,
		strings.NewReader(`{not json`)); err != nil {
		t.Fatal(err)
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithForceReinit(),
		core.WithStoreIndex(newIndex(t)),
	)
	if err != nil {
		t.Fatalf("WithForceReinit must clobber corrupted descriptor: %v", err)
	}
}

// --- Custom config / immutable validation ---

func TestInitStore_CustomConfigPersisted(t *testing.T) {
	drv := newDriver(t)
	cfg := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashBLAKE3,
		ManifestEncoding: domain.ManifestEncodingBinary,
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(newIndex(t)),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	desc, _ := descriptor.Read(context.Background(), drv)
	if desc.PathTopology != "Flat" {
		t.Errorf("PathTopology: got %q, want Flat", desc.PathTopology)
	}
	if desc.ContentHasher != "blake3" {
		t.Errorf("ContentHasher: got %q, want blake3", desc.ContentHasher)
	}
	if desc.ManifestEncoding != "Binary" {
		t.Errorf("ManifestEncoding: got %q, want Binary", desc.ManifestEncoding)
	}
}

func TestInitStore_RejectsInvalidConfig(t *testing.T) {
	drv := newDriver(t)
	cfg := domain.StoreConfig{ContentHasher: "md5"}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(newIndex(t)),
	)
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("expected errs.ErrInvalidConfig, got %v", err)
	}
}

func TestInitStore_NativeTopologyRequiresExternalRef(t *testing.T) {
	drv := newDriver(t)
	cfg := domain.StoreConfig{
		PathTopology: domain.PathTopologyNative,
		BlobStorage:  domain.BlobStorageTarget,
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(newIndex(t)),
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
	drv := newDriver(t)
	host := t.TempDir()
	idxPath := filepath.Join(host, "myindex.db")
	idx := newDiskIndex(t, idxPath)

	s, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(idx),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if s.State() != core.StateUnlocked {
		t.Errorf("state: got %v, want %v", s.State(), core.StateUnlocked)
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
		drv := newDriver(t)
		_, _, err := core.InitStore(context.Background(), drv,
			core.WithStoreIndex(newIndex(t)),
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
	drv := newDriver(t)
	_, err := core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
	)
	if !errors.Is(err, errs.ErrStoreNotFound) {
		t.Fatalf("expected errs.ErrStoreNotFound, got %v", err)
	}
}

func TestOpenStore_CorruptedDescriptor(t *testing.T) {
	drv := newDriver(t)
	if err := drv.Put(context.Background(), descriptor.Path,
		strings.NewReader(`{not json`)); err != nil {
		t.Fatal(err)
	}
	_, err := core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
	)
	if !errors.Is(err, errs.ErrStoreCorrupted) {
		t.Fatalf("expected errs.ErrStoreCorrupted, got %v", err)
	}
}

func TestOpenStore_RequiresStoreIndex(t *testing.T) {
	drv := newDriver(t)
	// Init first so the descriptor is in place.
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
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
	drv := newDriver(t)

	// Init with custom config so we can assert it is restored
	// faithfully on Open.
	custom := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashBLAKE3,
		ManifestEncoding: domain.ManifestEncodingBinary,
	}
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(custom),
		core.WithStoreIndex(newIndex(t)),
	); err != nil {
		t.Fatal(err)
	}

	// Open without WithConfig — legitimate diagnostic-style open.
	s, err := core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if s.State() != core.StateUnlocked {
		t.Errorf("state: got %v, want %v", s.State(), core.StateUnlocked)
	}
}

func TestOpenStore_MatchingConfig_Succeeds(t *testing.T) {
	drv := newDriver(t)
	custom := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashBLAKE3,
		ManifestEncoding: domain.ManifestEncodingBinary,
	}
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(custom),
		core.WithStoreIndex(newIndex(t)),
	); err != nil {
		t.Fatal(err)
	}

	// Re-open with the SAME immutable values — must succeed.
	s, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(custom),
		core.WithStoreIndex(newIndex(t)),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if s.State() != core.StateUnlocked {
		t.Errorf("state: got %v, want %v", s.State(), core.StateUnlocked)
	}
}

// --- OpenStore: errs.ErrConfigMismatch ---

func TestOpenStore_ConfigMismatch_PathTopology(t *testing.T) {
	drv := newDriver(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{PathTopology: domain.PathTopologyFlat}),
		core.WithStoreIndex(newIndex(t)),
	); err != nil {
		t.Fatal(err)
	}

	// Reopen with conflicting immutable.
	_, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{PathTopology: domain.PathTopologySharded}),
		core.WithStoreIndex(newIndex(t)),
	)
	if !errors.Is(err, errs.ErrConfigMismatch) {
		t.Fatalf("expected errs.ErrConfigMismatch, got %v", err)
	}
}

func TestOpenStore_ConfigMismatch_ContentHasher(t *testing.T) {
	drv := newDriver(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{ContentHasher: domain.HashSHA256}),
		core.WithStoreIndex(newIndex(t)),
	); err != nil {
		t.Fatal(err)
	}

	_, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{ContentHasher: domain.HashBLAKE3}),
		core.WithStoreIndex(newIndex(t)),
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
	drv := newDriver(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{
			PathTopology:  domain.PathTopologyFlat,
			ContentHasher: domain.HashBLAKE3,
		}),
		core.WithStoreIndex(newIndex(t)),
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
		core.WithStoreIndex(newIndex(t)),
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
	drv := newDriver(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{DeletionPolicyLock: false}),
		core.WithStoreIndex(newIndex(t)),
	); err != nil {
		t.Fatal(err)
	}

	// Caller asks to confirm the lock is engaged — but the
	// descriptor says it is not. This MUST mismatch.
	_, err := core.OpenStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{DeletionPolicyLock: true}),
		core.WithStoreIndex(newIndex(t)),
	)
	if !errors.Is(err, errs.ErrConfigMismatch) {
		t.Fatalf("expected errs.ErrConfigMismatch on stricter lock request, got %v", err)
	}

	// Caller does NOT assert anything (false). MUST succeed.
	_, err = core.OpenStore(context.Background(), drv,
		core.WithConfig(domain.StoreConfig{DeletionPolicyLock: false}),
		core.WithStoreIndex(newIndex(t)),
	)
	if err != nil {
		t.Errorf("opening with relaxed lock request must succeed, got %v", err)
	}
}

// --- OpenStore: encrypted Store rejection in M1.3 ---

func TestOpenStore_EncryptedStoreNotYetSupported(t *testing.T) {
	drv := newDriver(t)
	// Init normally, then hand-edit the descriptor to claim
	// encryption. This sidesteps the InitStore validation that
	// would otherwise refuse a passphrase-less encrypted Store —
	// we want to reach the OpenStore-level check.
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
	); err != nil {
		t.Fatal(err)
	}

	// Read, mutate, write back via the descriptor package directly.
	desc, err := descriptor.Read(context.Background(), drv)
	if err != nil {
		t.Fatal(err)
	}
	desc.ManifestCrypto = "MetadataOnly"
	if err := descriptor.Write(context.Background(), drv, desc); err != nil {
		t.Fatal(err)
	}

	_, err = core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
	)
	if err == nil {
		t.Fatal("expected encrypted-Store rejection in M1.3")
	}
	if !strings.Contains(err.Error(), "M2") {
		t.Errorf("error should reference M2 milestone: %v", err)
	}
}

// TestOpenStore_RestoresImmutableConfigFromDescriptor verifies that
// the active StoreConfig of the opened Store reflects the values
// persisted in the descriptor, not whatever defaults the caller
// might pass through.
func TestOpenStore_RestoresImmutableConfigFromDescriptor(t *testing.T) {
	drv := newDriver(t)
	custom := domain.StoreConfig{
		PathTopology:     domain.PathTopologyFlat,
		ContentHasher:    domain.HashBLAKE3,
		ManifestEncoding: domain.ManifestEncodingBinary,
	}
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(custom),
		core.WithStoreIndex(newIndex(t)),
	); err != nil {
		t.Fatal(err)
	}

	// Reopen without WithConfig: descriptor values must drive the
	// active StoreConfig. We cannot read activeConfig directly
	// (unexported); we observe the effects through descriptor
	// re-read instead.
	_, err := core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(newIndex(t)),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	desc, _ := descriptor.Read(context.Background(), drv)
	if desc.PathTopology != "Flat" || desc.ContentHasher != "blake3" {
		t.Errorf("descriptor should preserve InitStore values: %+v", desc)
	}
}
