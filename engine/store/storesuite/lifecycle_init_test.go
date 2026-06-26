// InitStore: fresh-location success, setup/existing/corrupt rejection
// (category 6 enumerable facts, as tables), and the encrypted-init
// contracts — recovery kit shape, descriptor crypto fields, kit↔descriptor
// agreement, KDF override, L1 replica write. OpenStore lives in
// lifecycle_open_test.go; config-validation boundaries are one table here.

package storesuite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/engine/driver"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/reconcile"
	"scrinium.dev/engine/store/internal/recoverykit"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// TestInitStore_FreshLocation_Succeeds: a fresh Plain init returns an
// Unlocked store with no recovery kit, writes a sequence-1 descriptor
// with a plaintext DEK, defaults the config (Plain / Sharded / sha256),
// and passes driver capabilities through.
func TestInitStore_FreshLocation_Succeeds(t *testing.T) {
	drv := driverfx.LocalFS(t)

	s, kit, err := store.InitStore(context.Background(), drv,
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
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

	desc, err := descriptor.Read(context.Background(), drv, storefx.Hashes())
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

	// Projection params live in store.config — read them via the active
	// config snapshot.
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
	if caps := s.Capabilities(); caps == 0 {
		t.Error("Capabilities returned zero — driver should expose flags")
	}
}

// TestInitStore_SetupRejected: InitStore refuses bad dependency setup. A
// nil driver fails; a missing StoreIndex fails with a message naming the
// option (dependency inversion — core never auto-builds an index).
func TestInitStore_SetupRejected(t *testing.T) {
	cases := []struct {
		name       string
		run        func(t *testing.T) error
		wantSubstr string // "" = any error
	}{
		{"nil driver", func(t *testing.T) error {
			_, _, err := store.InitStore(context.Background(), nil)
			return err
		}, ""},
		{"missing store index", func(t *testing.T) error {
			_, _, err := store.InitStore(context.Background(), driverfx.LocalFS(t),
				store.WithHashRegistry(storefx.Hashes()))
			return err
		}, "WithStoreIndex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(t)
			if err == nil {
				t.Fatalf("%s: expected an error", tc.name)
			}
			if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("%s: error %v should mention %q", tc.name, err, tc.wantSubstr)
			}
		})
	}
}

// TestInitStore_ExistingOrCorrupt: init over an existing store is refused
// (ErrStoreAlreadyExists); over a corrupt descriptor it is refused
// (ErrStoreCorrupted) unless WithForceReinit, which clobbers it.
func TestInitStore_ExistingOrCorrupt(t *testing.T) {
	initPlain := func(t *testing.T) driver.Driver {
		drv := driverfx.LocalFS(t)
		storefx.InitPlainOn(t, drv)
		return drv
	}
	corruptL0 := func(t *testing.T) driver.Driver {
		drv := driverfx.LocalFS(t)
		corruptDescriptorReplica(t, drv, descriptor.L0)
		return drv
	}
	cases := []struct {
		name  string
		setup func(t *testing.T) driver.Driver
		force bool
		want  error // nil = init must succeed
	}{
		{"already exists, no force", initPlain, false, errs.ErrStoreAlreadyExists},
		{"corrupt descriptor, no force", corruptL0, false, errs.ErrStoreCorrupted},
		{"corrupt descriptor, force clobbers", corruptL0, true, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drv := tc.setup(t)
			opts := []store.StoreOption{
				store.WithStoreIndex(indexfx.Memory(t)),
				store.WithHashRegistry(storefx.Hashes()),
			}
			if tc.force {
				opts = append(opts, store.WithForceReinit())
			}
			_, _, err := store.InitStore(context.Background(), drv, opts...)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("%s: got %v, want success", tc.name, err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestInitStore_ForceReinitRegeneratesStoreID: a forced re-init of a valid
// store succeeds, stays Unlocked, and mints a fresh StoreID (it does not
// reuse the old identity).
func TestInitStore_ForceReinitRegeneratesStoreID(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)
	desc1, _ := descriptor.Read(context.Background(), drv, storefx.Hashes())
	id1 := desc1.StoreID

	s2, _, err := store.InitStore(context.Background(), drv,
		store.WithForceReinit(),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("force-reinit InitStore: %v", err)
	}
	if s2.State() != domain.StateUnlocked {
		t.Errorf("state after force reinit: got %v, want %v", s2.State(), domain.StateUnlocked)
	}
	desc2, _ := descriptor.Read(context.Background(), drv, storefx.Hashes())
	if desc2.StoreID == id1 {
		t.Errorf("force reinit kept the same StoreID %q — should have generated a new one", id1)
	}
}

// TestInitStore_ConfigValidation: immutable-config bounds are enforced at
// init so the failure is loud, not deferred to the first Put. Binary
// encoding is refused until the MsgPack encoder lands (backlog §3.3);
// InlineBlobLimit caps at MaxInlineBlobLimit (docs/4 §5.6, beyond which
// hot index pages spill the SQLite cache); RetentionPeriod must be 0
// (feature off) or ≥ MinRetentionPeriod (below it the GC cycle outlives
// the window). Boundaries are inclusive.
func TestInitStore_ConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  domain.StoreConfig
		want error // nil = config must be accepted
	}{
		{"invalid content hasher", domain.StoreConfig{ContentHasher: "md5"}, errs.ErrInvalidConfig},
		{"binary manifest encoding", domain.StoreConfig{ManifestEncoding: domain.ManifestEncodingBinary}, errs.ErrInvalidConfig},
		{"inline blob limit over max", domain.StoreConfig{InlineBlobLimit: domain.MaxInlineBlobLimit + 1}, errs.ErrInvalidConfig},
		{"inline blob limit at max", domain.StoreConfig{InlineBlobLimit: domain.MaxInlineBlobLimit}, nil},
		{"retention period under min", domain.StoreConfig{RetentionPeriod: 30 * time.Minute}, errs.ErrInvalidConfig},
		{"retention period at min", domain.StoreConfig{RetentionPeriod: domain.MinRetentionPeriod}, nil},
		{"retention period zero", domain.StoreConfig{RetentionPeriod: 0}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, err := store.InitStore(context.Background(), driverfx.LocalFS(t),
				store.WithConfig(tc.cfg),
				store.WithStoreIndex(indexfx.Memory(t)),
				store.WithHashRegistry(storefx.Hashes()),
			)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("%s: got %v, want accepted", tc.name, err)
				}
				if s == nil {
					t.Fatalf("%s: accepted config returned nil Store", tc.name)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestInitStore_CustomConfigPersisted: caller-supplied immutable config is
// stored and reported back; unset fields take their defaults (here
// ManifestEncoding=JSON).
func TestInitStore_CustomConfigPersisted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cfg := domain.StoreConfig{
		PathTopology:  domain.PathTopologyFlat,
		ContentHasher: domain.HashBLAKE3,
	}
	s, _, err := store.InitStore(context.Background(), drv,
		store.WithConfig(cfg),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
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

// TestInitStore_DiskBackedIndex: the caller chooses where the index lives
// (dependency inversion); core neither overrides that path nor creates a
// default index inside the driver root.
func TestInitStore_DiskBackedIndex(t *testing.T) {
	drv := driverfx.LocalFS(t)
	host := t.TempDir()
	idxPath := filepath.Join(host, "myindex.db")
	idx := indexfx.Disk(t, idxPath)

	s, _, err := store.InitStore(context.Background(), drv,
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
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
	defaultPath := filepath.Join(drv.Root(), "index", "index.db")
	if _, err := os.Stat(defaultPath); err == nil {
		t.Errorf("core unexpectedly created %s", defaultPath)
	}
}

// TestInitStore_GeneratesUniqueStoreIDs: each init mints a distinct
// StoreID.
func TestInitStore_GeneratesUniqueStoreIDs(t *testing.T) {
	const N = 5
	ids := make(map[string]bool, N)
	for i := 0; i < N; i++ {
		drv := driverfx.LocalFS(t)
		_, _, err := store.InitStore(context.Background(), drv,
			store.WithStoreIndex(indexfx.Memory(t)),
			store.WithHashRegistry(storefx.Hashes()),
		)
		if err != nil {
			t.Fatal(err)
		}
		desc, _ := descriptor.Read(context.Background(), drv, storefx.Hashes())
		if ids[desc.StoreID] {
			t.Fatalf("duplicate StoreID %q", desc.StoreID)
		}
		ids[desc.StoreID] = true
	}
}

// --- Encrypted init ---

// TestInitStore_WithPassphrase_ReturnsRecoveryKit: an encrypted init
// returns a non-empty recovery kit.
func TestInitStore_WithPassphrase_ReturnsRecoveryKit(t *testing.T) {
	drv := driverfx.LocalFS(t)
	s, kit, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("hunter2")),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
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

// TestInitStore_WithPassphrase_DescriptorIsEncrypted: the on-disk
// descriptor wraps the DEK and records argon2id KDF params with a 16-byte
// salt.
func TestInitStore_WithPassphrase_DescriptorIsEncrypted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "hunter2")

	desc, err := descriptor.Read(context.Background(), drv, storefx.Hashes())
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

// TestInitStore_WithPassphrase_RecoveryKitIsValid: the kit decodes (its
// checksum and field shape verify) and carries the StoreID, argon2id
// algorithm, and a wrapped DEK.
func TestInitStore_WithPassphrase_RecoveryKitIsValid(t *testing.T) {
	drv := driverfx.LocalFS(t)
	_, kit, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("hunter2")),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
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

// TestInitStore_WithPassphrase_KitMatchesDescriptor: the kit and the
// on-disk descriptor agree byte-for-byte on the wrapped DEK and KDF
// params — a divergence would mean recovery could not unwrap the DEK.
func TestInitStore_WithPassphrase_KitMatchesDescriptor(t *testing.T) {
	drv := driverfx.LocalFS(t)
	_, kit, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("hunter2")),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := recoverykit.Decode(kit)
	desc, _ := descriptor.Read(context.Background(), drv, storefx.Hashes())

	if parsed.StoreID != desc.StoreID {
		t.Errorf("kit StoreID %q vs descriptor %q", parsed.StoreID, desc.StoreID)
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

// TestInitStore_PlainGeneratesPlaintextDEK locks in the §3.1 invariant: a
// Plain store still has a 32-byte DEK on disk, just unwrapped and with no
// KDF params — which makes a later SetPassphrase a pure wrap, no key
// generation.
func TestInitStore_PlainGeneratesPlaintextDEK(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)

	desc, _ := descriptor.Read(context.Background(), drv, storefx.Hashes())
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

// TestInitStore_NonPlainCryptoWithoutPassphrase: encrypted manifests with
// a plaintext DEK (the "worst of both worlds") is refused at init.
func TestInitStore_NonPlainCryptoWithoutPassphrase(t *testing.T) {
	for _, mc := range []domain.ManifestCrypto{
		domain.ManifestCryptoSealed,
		domain.ManifestCryptoParanoid,
	} {
		t.Run(string(mc), func(t *testing.T) {
			drv := driverfx.LocalFS(t)
			cfg := domain.StoreConfig{ManifestCrypto: mc}
			_, _, err := store.InitStore(context.Background(), drv,
				store.WithConfig(cfg),
				store.WithStoreIndex(indexfx.Memory(t)),
				store.WithHashRegistry(storefx.Hashes()),
			)
			if !errors.Is(err, errs.ErrPassphraseRequired) {
				t.Fatalf("expected ErrPassphraseRequired, got %v", err)
			}
		})
	}
}

// TestInitStore_PassphraseProviderError: a provider error surfaces wrapped
// as ErrPassphraseProvider.
func TestInitStore_PassphraseProviderError(t *testing.T) {
	drv := driverfx.LocalFS(t)
	sentinel := errors.New("user cancelled")
	failing := func(_ context.Context, _ domain.PassphraseHint) ([]byte, error) {
		return nil, sentinel
	}
	_, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(failing),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrPassphraseProvider) {
		t.Fatalf("expected ErrPassphraseProvider, got %v", err)
	}
}

// TestInitStore_HintCarriesInitReason: init calls the passphrase provider
// with Reason="init" and a fresh StoreID, so providers may dispatch on it.
func TestInitStore_HintCarriesInitReason(t *testing.T) {
	drv := driverfx.LocalFS(t)
	var seenHint domain.PassphraseHint
	provider := func(_ context.Context, h domain.PassphraseHint) ([]byte, error) {
		seenHint = h
		return []byte("pw"), nil
	}
	_, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(provider),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
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

// TestInitStore_KDFParamsOverride: StoreConfig.KDFParams cost knobs reach
// the on-disk descriptor.
func TestInitStore_KDFParamsOverride(t *testing.T) {
	drv := driverfx.LocalFS(t)
	cost := &domain.KDFParams{Time: 2, Memory: 32 * 1024, Threads: 2}
	cfg := domain.StoreConfig{KDFParams: cost}
	_, _, err := store.InitStore(context.Background(), drv,
		store.WithConfig(cfg),
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	desc, _ := descriptor.Read(context.Background(), drv, storefx.Hashes())
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

// TestInitStore_WritesL1Replica: init persists both replicas — the L1
// shadow is present, valid, and mirrors L0's encrypted state.
func TestInitStore_WritesL1Replica(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "pw")

	d, status, err := reconcile.ReadReplica(context.Background(), drv, storefx.Hashes(), descriptor.L1)
	if err != nil {
		t.Fatalf("ReadReplica L1: %v", err)
	}
	if status != reconcile.Valid {
		t.Errorf("L1 status: got %v, want Valid", status)
	}
	if !d.DEKEncrypted {
		t.Error("L1 DEKEncrypted should mirror L0")
	}
}
