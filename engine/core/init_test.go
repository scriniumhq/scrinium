package core_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"scrinium.dev/engine/core"
	"scrinium.dev/engine/core/internal/descriptor"
	"scrinium.dev/engine/core/internal/recoverykit"
	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/testutil/storefx"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
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
	storefx.InitPlainOn(t, drv)
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
	storefx.InitPlainOn(t, drv)
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
		PathTopology:  domain.PathTopologyFlat,
		ContentHasher: domain.HashBLAKE3,
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
	if got.ManifestEncoding != "JSON" {
		t.Errorf("ManifestEncoding: got %q, want JSON (default)", got.ManifestEncoding)
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

// TestInitStore_RejectsBinaryManifestEncoding closes the gap between
// the validate side (used to accept Binary) and the codec side (which
// has always rejected it with ErrUnsupportedEncoding). Until the
// MsgPack encoder lands (backlog §3.3) we refuse Binary at config
// validation so the failure is loud at InitStore, not deferred to
// the first user Put.
func TestInitStore_RejectsBinaryManifestEncoding(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{ManifestEncoding: domain.ManifestEncodingBinary}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("expected errs.ErrInvalidConfig, got %v", err)
	}
}

// TestInitStore_RejectsInlineBlobLimitTooLarge verifies the upper
// bound from docs/4 §5.6: InlineBlobLimit > 64 KiB pushes hot index
// pages out of SQLite page cache. Zero is still accepted as the
// "feature off" value.
func TestInitStore_RejectsInlineBlobLimitTooLarge(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{InlineBlobLimit: domain.MaxInlineBlobLimit + 1}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("expected errs.ErrInvalidConfig, got %v", err)
	}
}

// TestInitStore_AcceptsInlineBlobLimitAtBoundary covers the inclusive
// end of the range: the documented limit itself is allowed.
func TestInitStore_AcceptsInlineBlobLimitAtBoundary(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{InlineBlobLimit: domain.MaxInlineBlobLimit}
	s, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("InlineBlobLimit at boundary should be accepted, got %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil Store")
	}
}

// TestInitStore_RejectsRetentionPeriodTooShort verifies the lower
// bound from docs/4 §5.6: a non-zero RetentionPeriod under 1h is
// pointless because the GC cycle outlives the retention window.
// Zero is still accepted as the "feature off" value.
func TestInitStore_RejectsRetentionPeriodTooShort(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{RetentionPeriod: 30 * time.Minute}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("expected errs.ErrInvalidConfig, got %v", err)
	}
}

// TestInitStore_AcceptsRetentionPeriodAtBoundary covers the inclusive
// start of the range: exactly 1h is allowed.
func TestInitStore_AcceptsRetentionPeriodAtBoundary(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{RetentionPeriod: domain.MinRetentionPeriod}
	s, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("RetentionPeriod at boundary should be accepted, got %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil Store")
	}
}

// TestInitStore_AcceptsRetentionPeriodZero confirms that the
// "feature off" semantics survive: zero RetentionPeriod must not
// trigger the >= MinRetentionPeriod check.
func TestInitStore_AcceptsRetentionPeriodZero(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{RetentionPeriod: 0}
	s, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("RetentionPeriod=0 should be accepted, got %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil Store")
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
	_, err := storefx.TryOpenOn(t, drv)
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
	_, err := storefx.TryOpenOn(t, drv)
	if !errors.Is(err, errs.ErrStoreCorrupted) {
		t.Fatalf("expected errs.ErrStoreCorrupted, got %v", err)
	}
}

func TestOpenStore_RequiresStoreIndex(t *testing.T) {
	drv := driverfx.LocalFS(t)
	// Init first so the descriptor is in place.
	storefx.InitPlainOn(t, drv)
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
		PathTopology:  domain.PathTopologyFlat,
		ContentHasher: domain.HashBLAKE3,
	}
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(custom),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	// Open without WithConfig — legitimate diagnostic-style open.
	s, err := storefx.TryOpenOn(t, drv)
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
		PathTopology:  domain.PathTopologyFlat,
		ContentHasher: domain.HashBLAKE3,
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

// --- OpenStore: non-Plain ManifestCrypto pending M2.3 ---

// TestOpenStore_NonPlainManifestCryptoOpens verifies that
// OpenStore accepts MetadataOnly and Envelope configurations.
// The body-encryption path itself is exercised by Put/Get
// integration tests; this test only checks that OpenStore no
// longer refuses such configurations.
//
// Note: ManifestStorage Local/Replicated lands in M4.2 alongside
// HostStorage; here we use Remote (the default) so the test
// stays scoped to ManifestCrypto.
func TestOpenStore_NonPlainManifestCryptoOpens(t *testing.T) {
	for _, crypto := range []domain.ManifestCrypto{
		domain.ManifestCryptoMetadataOnly,
		domain.ManifestCryptoEnvelope,
	} {
		t.Run(string(crypto), func(t *testing.T) {
			drv := driverfx.LocalFS(t)
			idx := indexfx.Memory(t)
			cfg := domain.StoreConfig{ManifestCrypto: crypto}

			if _, _, err := core.InitStore(context.Background(), drv,
				core.WithConfig(cfg),
				core.WithPassphrase(storefx.StaticPP("pw")),
				core.WithStoreIndex(idx),
				core.WithHashRegistry(storefx.Hashes()),
			); err != nil {
				t.Fatalf("InitStore: %v", err)
			}

			s, err := core.OpenStore(context.Background(), drv,
				core.WithConfig(cfg),
				core.WithPassphrase(storefx.StaticPP("pw")),
				core.WithAutoUnlock(),
				core.WithStoreIndex(idx),
				core.WithHashRegistry(storefx.Hashes()),
			)
			if err != nil {
				t.Fatalf("OpenStore: %v", err)
			}
			if s.State() != domain.StateUnlocked {
				t.Errorf("State: got %v, want Unlocked", s.State())
			}
		})
	}
}

// TestOpenStore_RestoresImmutableConfigFromSystemConfig verifies that
// the active StoreConfig of the opened Store reflects the values
// persisted in system.config, not whatever defaults the caller
// might pass through.
func TestOpenStore_RestoresImmutableConfigFromSystemConfig(t *testing.T) {
	drv := driverfx.LocalFS(t)
	custom := domain.StoreConfig{
		PathTopology:  domain.PathTopologyFlat,
		ContentHasher: domain.HashBLAKE3,
	}
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(custom),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	s, err := storefx.TryOpenOn(t, drv)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	got := s.Config()
	if got.PathTopology != "Flat" || got.ContentHasher != "blake3" {
		t.Errorf("active config should preserve InitStore values: %+v", got)
	}
}

// --- Encrypted Store init (M2.2) ---

func TestInitStore_WithPassphrase_ReturnsRecoveryKit(t *testing.T) {
	drv := driverfx.LocalFS(t)

	s, kit, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(storefx.StaticPP("hunter2")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if s == nil {
		t.Fatal("nil Store returned")
	}
	if len(kit) == 0 {
		t.Fatal("Recovery Kit must be non-empty for encrypted Store")
	}
}

func TestInitStore_WithPassphrase_DescriptorIsEncrypted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "hunter2")

	desc, err := descriptor.Read(context.Background(), drv)
	if err != nil {
		t.Fatalf("read descriptor: %v", err)
	}
	if !desc.DEKEncrypted {
		t.Error("DEKEncrypted should be true")
	}
	if len(desc.DEK) == 0 {
		t.Error("DEK should be non-empty (wrapped)")
	}
	if desc.KDFParams == nil {
		t.Fatal("KDFParams should be present")
	}
	if desc.KDFParams.Algorithm != "argon2id" {
		t.Errorf("KDF Algorithm: got %q, want argon2id", desc.KDFParams.Algorithm)
	}
	if len(desc.KDFParams.Salt) != 16 {
		t.Errorf("KDF Salt length: got %d, want 16", len(desc.KDFParams.Salt))
	}
}

func TestInitStore_WithPassphrase_RecoveryKitIsValid(t *testing.T) {
	drv := driverfx.LocalFS(t)
	_, kit, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(storefx.StaticPP("hunter2")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Decoding the kit verifies its checksum and field shape.
	parsed, err := recoverykit.Decode(kit)
	if err != nil {
		t.Fatalf("Decode kit: %v", err)
	}
	if parsed.StoreID == "" {
		t.Error("kit StoreID empty")
	}
	if parsed.Algorithm != "argon2id" {
		t.Errorf("kit Algorithm: got %q, want argon2id", parsed.Algorithm)
	}
	if len(parsed.EncryptedDEK) == 0 {
		t.Error("kit EncryptedDEK empty")
	}
}

// TestInitStore_WithPassphrase_KitMatchesDescriptor verifies that
// the kit and the on-disk descriptor agree byte-for-byte on the
// wrapped DEK and the KDF parameters. A divergence here would
// mean a recovery operation could not actually unwrap the DEK.
func TestInitStore_WithPassphrase_KitMatchesDescriptor(t *testing.T) {
	drv := driverfx.LocalFS(t)
	_, kit, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(storefx.StaticPP("hunter2")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	parsed, _ := recoverykit.Decode(kit)
	desc, _ := descriptor.Read(context.Background(), drv)

	if parsed.StoreID != desc.StoreID {
		t.Errorf("kit StoreID %q vs descriptor %q",
			parsed.StoreID, desc.StoreID)
	}
	if !bytes.Equal(parsed.EncryptedDEK, desc.DEK) {
		t.Error("kit EncryptedDEK differs from descriptor.DEK")
	}
	if !bytes.Equal(parsed.Salt, desc.KDFParams.Salt) {
		t.Error("kit Salt differs from descriptor.KDFParams.Salt")
	}
	if parsed.Time != desc.KDFParams.Time ||
		parsed.Memory != desc.KDFParams.Memory ||
		parsed.Threads != desc.KDFParams.Threads {
		t.Error("kit cost params differ from descriptor.KDFParams")
	}
}

// TestInitStore_PlainGeneratesPlaintextDEK locks in the §3.1
// invariant: a Plain Store still has a DEK on disk, just
// unwrapped. This makes future SetPassphrase trivial — wrap the
// existing DEK, no key generation needed.
func TestInitStore_PlainGeneratesPlaintextDEK(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)

	desc, _ := descriptor.Read(context.Background(), drv)
	if desc.DEKEncrypted {
		t.Error("DEKEncrypted should be false for Plain Store")
	}
	if len(desc.DEK) != 32 {
		t.Errorf("plaintext DEK length: got %d, want 32", len(desc.DEK))
	}
	if desc.KDFParams != nil {
		t.Error("KDFParams should be nil for Plain Store")
	}
}

// TestInitStore_NonPlainCryptoWithoutPassphrase verifies the
// "worst of both worlds" guard: encrypted manifests + plaintext
// DEK is refused at InitStore.
func TestInitStore_NonPlainCryptoWithoutPassphrase(t *testing.T) {
	for _, mc := range []domain.ManifestCrypto{
		domain.ManifestCryptoMetadataOnly,
		domain.ManifestCryptoEnvelope,
	} {
		t.Run(string(mc), func(t *testing.T) {
			drv := driverfx.LocalFS(t)
			cfg := domain.StoreConfig{ManifestCrypto: mc}
			_, _, err := core.InitStore(context.Background(), drv,
				core.WithConfig(cfg),
				core.WithStoreIndex(indexfx.Memory(t)),
				core.WithHashRegistry(storefx.Hashes()),
			)
			if !errors.Is(err, errs.ErrPassphraseRequired) {
				t.Fatalf("expected ErrPassphraseRequired, got %v", err)
			}
		})
	}
}

// TestInitStore_PassphraseProviderError surfaces the provider's
// error wrapped with ErrPassphraseProvider.
func TestInitStore_PassphraseProviderError(t *testing.T) {
	drv := driverfx.LocalFS(t)
	sentinel := errors.New("user cancelled")
	failing := func(_ context.Context, _ core.PassphraseHint) ([]byte, error) {
		return nil, sentinel
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(failing),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrPassphraseProvider) {
		t.Fatalf("expected ErrPassphraseProvider, got %v", err)
	}
}

// TestInitStore_HintCarriesInitReason locks in the contract that
// InitStore calls the provider with Reason="init" and a fresh
// StoreID. Future PassphraseProviders may dispatch on Reason.
func TestInitStore_HintCarriesInitReason(t *testing.T) {
	drv := driverfx.LocalFS(t)
	var seenHint core.PassphraseHint
	provider := func(_ context.Context, h core.PassphraseHint) ([]byte, error) {
		seenHint = h
		return []byte("pw"), nil
	}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(provider),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if seenHint.Reason != "init" {
		t.Errorf("Reason: got %q, want %q", seenHint.Reason, "init")
	}
	if seenHint.StoreID == "" {
		t.Error("StoreID should be non-empty in hint")
	}
}

// TestInitStore_KDFParamsOverride verifies that
// StoreConfig.KDFParams (cost knobs) end up in the on-disk
// descriptor.
func TestInitStore_KDFParamsOverride(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cost := &domain.KDFParams{Time: 2, Memory: 32 * 1024, Threads: 2}
	cfg := domain.StoreConfig{KDFParams: cost}
	_, _, err := core.InitStore(context.Background(), drv,
		core.WithConfig(cfg),
		core.WithPassphrase(storefx.StaticPP("pw")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	desc, _ := descriptor.Read(context.Background(), drv)
	if desc.KDFParams.Time != 2 {
		t.Errorf("Time: got %d, want 2", desc.KDFParams.Time)
	}
	if desc.KDFParams.Memory != 32*1024 {
		t.Errorf("Memory: got %d, want %d", desc.KDFParams.Memory, 32*1024)
	}
	if desc.KDFParams.Threads != 2 {
		t.Errorf("Threads: got %d, want 2", desc.KDFParams.Threads)
	}
}

// TestInitStore_WritesL1Replica verifies that Persist writes both
// L0 and L1, not just L0. Reading from BackupPath through
// ReadReplica is the simplest check.
func TestInitStore_WritesL1Replica(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "pw")

	d, status, err := descriptor.ReadReplica(context.Background(), drv, descriptor.BackupPath)
	if err != nil {
		t.Fatalf("ReadReplica L1: %v", err)
	}
	if status != descriptor.ReplicaValid {
		t.Errorf("L1 status: got %v, want ReplicaValid", status)
	}
	if !d.DEKEncrypted {
		t.Error("L1 DEKEncrypted should mirror L0")
	}
}
