package core_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/core/internal/descriptor"
	"github.com/rkurbatov/scrinium/core/internal/recoverykit"
	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/internal/testutil/driverfx"
	"github.com/rkurbatov/scrinium/internal/testutil/indexfx"
	"github.com/rkurbatov/scrinium/internal/testutil/storefx"
)

// --- Test helpers ---

// rotatingProvider returns a different passphrase per call,
// driven by a slice of values. Used to script provider behaviour
// across two-call methods (RotateKEK).
func rotatingProvider(values ...string) core.PassphraseProvider {
	i := 0
	return func(_ context.Context, _ core.PassphraseHint) ([]byte, error) {
		if i >= len(values) {
			return nil, errors.New("rotatingProvider: ran out of values")
		}
		v := values[i]
		i++
		return []byte(v), nil
	}
}

// hintCapturingProvider returns the configured passphrase but
// records every PassphraseHint it sees. Used to verify Reason /
// NeedNew on the wire.
func hintCapturingProvider(pass string, log *[]core.PassphraseHint) core.PassphraseProvider {
	return func(_ context.Context, h core.PassphraseHint) ([]byte, error) {
		*log = append(*log, h)
		return []byte(pass), nil
	}
}

// initEncryptedAndOpenLocked: bootstrap an encrypted Store, then
// reopen it WITHOUT auto-unlock so the returned Store is in
// StateLocked. The provider is captured on the second open so
// subsequent Unlock calls can use it.
func initEncryptedAndOpenLocked(t *testing.T, pass string) (core.Store, core.PassphraseProvider) {
	t.Helper()
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(staticPP(pass)),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	provider := staticPP(pass)
	s, err := core.OpenStore(context.Background(), drv,
		core.WithPassphrase(provider),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if s.State() != domain.StateLocked {
		t.Fatalf("setup: expected StateLocked, got %v", s.State())
	}
	return s, provider
}

// initPlain returns a brand-new Plain Store (Unlocked) on a
// fresh Driver.
func initPlain(t *testing.T) core.Store {
	t.Helper()
	drv := driverfx.LocalFS(t)
	s, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	return s
}

// --- Unlock ---

func TestUnlock_LockedStoreSucceeds(t *testing.T) {
	s, _ := initEncryptedAndOpenLocked(t, "hunter2")

	if err := s.Unlock(context.Background()); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if s.State() != domain.StateUnlocked {
		t.Errorf("State after Unlock: got %v, want Unlocked", s.State())
	}
}

func TestUnlock_AlreadyUnlockedIsNoOp(t *testing.T) {
	s := initPlain(t)
	// Call Unlock on an Unlocked Plain Store. Must succeed
	// without prompting the (nil) provider.
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatalf("Unlock idempotent: %v", err)
	}
	if s.State() != domain.StateUnlocked {
		t.Errorf("State should stay Unlocked, got %v", s.State())
	}
}

func TestUnlock_DoubleCallIsIdempotent(t *testing.T) {
	s, _ := initEncryptedAndOpenLocked(t, "pw")
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatalf("second Unlock should be no-op: %v", err)
	}
}

func TestUnlock_WrongPassphrase(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(staticPP("right")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	s, err := core.OpenStore(context.Background(), drv,
		core.WithPassphrase(staticPP("wrong")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	err = s.Unlock(context.Background())
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
	// State must remain Locked after failed unlock.
	if s.State() != domain.StateLocked {
		t.Errorf("State after failed Unlock: got %v, want Locked", s.State())
	}
}

func TestUnlock_NoProvider(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(staticPP("pw")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Open without provider — Store goes to Locked, but Unlock
	// has nothing to call.
	s, err := core.OpenStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Unlock(context.Background())
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
}

func TestUnlock_HintCarriesUnlockReason(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(staticPP("pw")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	var hints []core.PassphraseHint
	s, err := core.OpenStore(context.Background(), drv,
		core.WithPassphrase(hintCapturingProvider("pw", &hints)),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 {
		t.Fatalf("expected 1 provider call, got %d", len(hints))
	}
	if hints[0].Reason != "unlock" {
		t.Errorf("Reason: got %q, want unlock", hints[0].Reason)
	}
	if hints[0].NeedNew {
		t.Error("NeedNew should be false for unlock")
	}
}

// --- SetPassphrase ---

func TestSetPassphrase_PlainStoreBecomesEncrypted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Reopen with the SAME idx so Orphan Scan finds known
	// manifests and leaves them in place.
	s, err := core.OpenStore(context.Background(), drv,
		core.WithPassphrase(staticPP("new-pw")),
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.SetPassphrase(context.Background()); err != nil {
		t.Fatalf("SetPassphrase: %v", err)
	}

	// Descriptor on disk reflects the change.
	desc, _ := descriptor.Read(context.Background(), drv)
	if !desc.DEKEncrypted {
		t.Error("descriptor.DEKEncrypted should be true after SetPassphrase")
	}
	if desc.KDFParams == nil {
		t.Error("KDFParams should be present")
	}
	if desc.Sequence != 2 {
		t.Errorf("Sequence: got %d, want 2", desc.Sequence)
	}

	// Reopen with the new passphrase must succeed. Same idx.
	if _, err = core.OpenStore(context.Background(), drv,
		core.WithPassphrase(staticPP("new-pw")),
		core.WithAutoUnlock(),
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Errorf("OpenStore with new pw: %v", err)
	}
}

func TestSetPassphrase_RejectsAlreadyEncrypted(t *testing.T) {
	s, _ := initEncryptedAndOpenLocked(t, "pw")
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := s.SetPassphrase(context.Background())
	if !errors.Is(err, errs.ErrPassphraseAlreadySet) {
		t.Fatalf("expected ErrPassphraseAlreadySet, got %v", err)
	}
}

func TestSetPassphrase_NoProvider(t *testing.T) {
	s := initPlain(t)
	err := s.SetPassphrase(context.Background())
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
}

func TestSetPassphrase_HintReason(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	var hints []core.PassphraseHint
	s, err := core.OpenStore(context.Background(), drv,
		core.WithPassphrase(hintCapturingProvider("new-pw", &hints)),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPassphrase(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(hints) != 1 {
		t.Fatalf("expected 1 provider call, got %d", len(hints))
	}
	if hints[0].Reason != "set_passphrase" {
		t.Errorf("Reason: got %q, want set_passphrase", hints[0].Reason)
	}
}

// --- RotateKEK ---

func TestRotateKEK_RotatesPassphrase(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(staticPP("old-pw")),
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Provider is called THREE times in this flow:
	//   1. AutoUnlock at OpenStore — needs the current "old-pw".
	//   2. RotateKEK current-pass verify — "old-pw" again.
	//   3. RotateKEK new-pass — supplies "new-pw".
	s, err := core.OpenStore(context.Background(), drv,
		core.WithPassphrase(rotatingProvider("old-pw", "old-pw", "new-pw")),
		core.WithAutoUnlock(),
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RotateKEK(context.Background()); err != nil {
		t.Fatalf("RotateKEK: %v", err)
	}

	desc, _ := descriptor.Read(context.Background(), drv)
	if desc.Sequence < 2 {
		t.Errorf("Sequence after rotation: got %d, want >= 2", desc.Sequence)
	}

	// Reopen with old passphrase must fail; with new must succeed.
	// Same idx so Orphan Scan keeps the system.config manifest.
	_, err = core.OpenStore(context.Background(), drv,
		core.WithPassphrase(staticPP("old-pw")),
		core.WithAutoUnlock(),
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Errorf("OpenStore with old pw: expected ErrDecryptionFailed, got %v", err)
	}

	if _, err = core.OpenStore(context.Background(), drv,
		core.WithPassphrase(staticPP("new-pw")),
		core.WithAutoUnlock(),
		core.WithStoreIndex(idx),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Errorf("OpenStore with new pw: %v", err)
	}
}

func TestRotateKEK_WrongCurrentPassphrase(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(staticPP("right")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Provider returns "wrong" first (current verify fails),
	// then "new" (never reached).
	s, err := core.OpenStore(context.Background(), drv,
		core.WithPassphrase(rotatingProvider("right", "wrong", "new")),
		core.WithAutoUnlock(),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	// AutoUnlock consumed the first "right" already. RotateKEK
	// will see "wrong" → fail.
	err = s.RotateKEK(context.Background())
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestRotateKEK_RejectsPlainStore(t *testing.T) {
	s := initPlain(t)
	err := s.RotateKEK(context.Background())
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired (use SetPassphrase), got %v", err)
	}
}

func TestRotateKEK_HintReasonsAndNeedNew(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(staticPP("pw")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	var hints []core.PassphraseHint
	s, err := core.OpenStore(context.Background(), drv,
		core.WithPassphrase(hintCapturingProvider("pw", &hints)),
		core.WithAutoUnlock(),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	// Drop hints captured during AutoUnlock.
	hints = hints[:0]

	if err := s.RotateKEK(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(hints) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(hints))
	}
	if hints[0].Reason != "kek_rotation" || hints[0].NeedNew {
		t.Errorf("first hint: got %+v, want Reason=kek_rotation, NeedNew=false", hints[0])
	}
	if hints[1].Reason != "kek_rotation" || !hints[1].NeedNew {
		t.Errorf("second hint: got %+v, want Reason=kek_rotation, NeedNew=true", hints[1])
	}
}

// --- ExportRecoveryKit ---

func TestExportRecoveryKit_EncryptedStoreReturnsKit(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(staticPP("pw")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	s, err := core.OpenStore(context.Background(), drv,
		core.WithPassphrase(staticPP("pw")),
		core.WithAutoUnlock(),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	kit, err := s.ExportRecoveryKit(context.Background())
	if err != nil {
		t.Fatalf("ExportRecoveryKit: %v", err)
	}
	if len(kit) == 0 {
		t.Fatal("kit should be non-empty")
	}

	// Validate via Decode.
	parsed, err := recoverykit.Decode(kit)
	if err != nil {
		t.Fatalf("Decode kit: %v", err)
	}

	desc, _ := descriptor.Read(context.Background(), drv)
	if !bytes.Equal(parsed.EncryptedDEK, desc.DEK) {
		t.Error("kit EncryptedDEK should match descriptor.DEK")
	}
}

func TestExportRecoveryKit_PlainStoreRejected(t *testing.T) {
	s := initPlain(t)
	_, err := s.ExportRecoveryKit(context.Background())
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
}

func TestExportRecoveryKit_LockedStoreRejected(t *testing.T) {
	s, _ := initEncryptedAndOpenLocked(t, "pw")
	_, err := s.ExportRecoveryKit(context.Background())
	if err == nil {
		t.Fatal("expected error on Locked Store")
	}
}

func TestExportRecoveryKit_RegeneratesAfterRotation(t *testing.T) {
	drv := driverfx.LocalFS(t)
	if _, _, err := core.InitStore(context.Background(), drv,
		core.WithPassphrase(staticPP("old")),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	s, err := core.OpenStore(context.Background(), drv,
		core.WithPassphrase(rotatingProvider("old", "old", "new")),
		core.WithAutoUnlock(),
		core.WithStoreIndex(indexfx.Memory(t)),
		core.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	kitBefore, _ := s.ExportRecoveryKit(context.Background())
	if err := s.RotateKEK(context.Background()); err != nil {
		t.Fatal(err)
	}
	kitAfter, _ := s.ExportRecoveryKit(context.Background())

	if bytes.Equal(kitBefore, kitAfter) {
		t.Error("kit must change after RotateKEK")
	}

	// Old kit decodes — but wraps DEK with the OLD KEK.
	// New kit decodes with NEW KEK. Verifying the cryptographic
	// part is over the line for this test; we just check the
	// content differs.
	parsed, err := recoverykit.Decode(kitAfter)
	if err != nil {
		t.Fatalf("Decode kit: %v", err)
	}
	desc, _ := descriptor.Read(context.Background(), drv)
	if !bytes.Equal(parsed.EncryptedDEK, desc.DEK) {
		t.Error("post-rotation kit must reflect new wrapped DEK")
	}
}
