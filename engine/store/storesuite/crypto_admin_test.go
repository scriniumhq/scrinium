package storesuite_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/recoverykit"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/driverfx"
	"scrinium.dev/testutil/indexfx"
	"scrinium.dev/testutil/storefx"
)

// --- Unlock ---

func TestUnlock_LockedStoreSucceeds(t *testing.T) {
	s, _ := storefx.InitEncryptedLocked(t, "hunter2")

	if err := s.Unlock(context.Background()); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if s.State() != domain.StateUnlocked {
		t.Errorf("State after Unlock: got %v, want Unlocked", s.State())
	}
}

func TestUnlock_AlreadyUnlockedIsNoOp(t *testing.T) {
	s, _ := storefx.InitPlain(t)
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
	s, _ := storefx.InitEncryptedLocked(t, "pw")
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatalf("second Unlock should be no-op: %v", err)
	}
}

func TestUnlock_WrongPassphrase(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "right")
	s, err := storefx.TryOpenOn(t, drv,
		store.WithPassphrase(storefx.StaticPP("wrong")),
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
	storefx.InitEncryptedOn(t, drv, "pw")
	// Open without provider — Store goes to Locked, but Unlock
	// has nothing to call.
	s, err := storefx.TryOpenOn(t, drv)
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
	storefx.InitEncryptedOn(t, drv, "pw")
	var hints []domain.PassphraseHint
	s, err := storefx.TryOpenOn(t, drv,
		store.WithPassphrase(storefx.RecordingPP("pw", &hints)),
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
}

// --- SetPassphrase ---

func TestSetPassphrase_PlainStoreBecomesEncrypted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Reopen with the SAME idx so Orphan Scan finds known
	// manifests and leaves them in place.
	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("new-pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
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
	if _, err = store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("new-pw")),
		store.WithAutoUnlock(),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Errorf("OpenStore with new pw: %v", err)
	}
}

func TestSetPassphrase_RejectsAlreadyEncrypted(t *testing.T) {
	s, _ := storefx.InitEncryptedLocked(t, "pw")
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	err := s.SetPassphrase(context.Background())
	if !errors.Is(err, errs.ErrPassphraseAlreadySet) {
		t.Fatalf("expected ErrPassphraseAlreadySet, got %v", err)
	}
}

func TestSetPassphrase_NoProvider(t *testing.T) {
	s, _ := storefx.InitPlain(t)
	err := s.SetPassphrase(context.Background())
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
}

func TestSetPassphrase_HintReason(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitPlainOn(t, drv)
	var hints []domain.PassphraseHint
	s, err := storefx.TryOpenOn(t, drv,
		store.WithPassphrase(storefx.RecordingPP("new-pw", &hints)),
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
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("old-pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Provider is called THREE times in this flow:
	//   1. AutoUnlock at OpenStore — needs the current "old-pw".
	//   2. RotateKEK current-pass verify — "old-pw" again.
	//   3. RotateKEK new-pass — supplies "new-pw".
	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.ScriptedPP("old-pw", "old-pw", "new-pw")),
		store.WithAutoUnlock(),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
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
	_, err = store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("old-pw")),
		store.WithAutoUnlock(),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Errorf("OpenStore with old pw: expected ErrDecryptionFailed, got %v", err)
	}

	if _, err = store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("new-pw")),
		store.WithAutoUnlock(),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Errorf("OpenStore with new pw: %v", err)
	}
}

func TestRotateKEK_WrongCurrentPassphrase(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "right")
	// Provider returns "wrong" first (current verify fails),
	// then "new" (never reached).
	s, err := storefx.TryOpenOn(t, drv,
		store.WithPassphrase(storefx.ScriptedPP("right", "wrong", "new")),
		store.WithAutoUnlock(),
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
	s, _ := storefx.InitPlain(t)
	err := s.RotateKEK(context.Background())
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired (use SetPassphrase), got %v", err)
	}
}

func TestRotateKEK_HintReasons(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "pw")
	var hints []domain.PassphraseHint
	s, err := storefx.TryOpenOn(t, drv,
		store.WithPassphrase(storefx.RecordingPP("pw", &hints)),
		store.WithAutoUnlock(),
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
	// First call retrieves the current passphrase. Reason="unlock"
	// matches the lookup hosts use during Store.Unlock — keychain
	// integrations that key off Reason find the cached entry.
	if hints[0].Reason != "unlock" {
		t.Errorf("first hint: got %+v, want Reason=unlock", hints[0])
	}
	// Second call retrieves the new passphrase. Reason="kek_rotation"
	// is unique to RotateKEK and signals "this is a rotation in progress".
	if hints[1].Reason != "kek_rotation" {
		t.Errorf("second hint: got %+v, want Reason=kek_rotation", hints[1])
	}
}

// --- ExportRecoveryKit ---

func TestExportRecoveryKit_EncryptedStoreReturnsKit(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "pw")
	s, err := storefx.TryOpenOn(t, drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
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
	s, _ := storefx.InitPlain(t)
	_, err := s.ExportRecoveryKit(context.Background())
	if !errors.Is(err, errs.ErrPassphraseRequired) {
		t.Fatalf("expected ErrPassphraseRequired, got %v", err)
	}
}

func TestExportRecoveryKit_LockedStoreRejected(t *testing.T) {
	s, _ := storefx.InitEncryptedLocked(t, "pw")
	_, err := s.ExportRecoveryKit(context.Background())
	if err == nil {
		t.Fatal("expected error on Locked Store")
	}
}

func TestExportRecoveryKit_RegeneratesAfterRotation(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "old")
	s, err := storefx.TryOpenOn(t, drv,
		store.WithPassphrase(storefx.ScriptedPP("old", "old", "new")),
		store.WithAutoUnlock(),
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

// TestRotateKEK_NewPassphraseProviderError covers the failure path where
// the provider succeeds for the current-passphrase prompts (AutoUnlock +
// the current-pass verify) but errors on the new passphrase. RotateKEK
// must surface that provider error, not mask it.
//
// Regression guard: the new-pass callProvider error used to be
// overwritten by WrapDEK's err. callProvider returns nil on error, so
// WrapDEK then saw an empty passphrase and returned ErrPassphraseRequired
// — swallowing the provider's real failure and pointing the operator at
// the wrong cause. (No lockout: WrapDEK's empty-passphrase guard fires
// before commitDescriptor, so the descriptor is never rewritten.) The
// fix adds the missing error check; see store.RotateKEK "new passphrase".
//
// Unfixed  → ErrPassphraseRequired (masked).
// Fixed    → ErrPassphraseProvider (real cause surfaces).
func TestRotateKEK_NewPassphraseProviderError(t *testing.T) {
	ctx := context.Background()
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)

	if _, _, err := store.InitStore(ctx, drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	// Answers every "unlock" prompt (AutoUnlock, then the current-pass
	// verify inside RotateKEK) with the real passphrase, but fails the
	// "kek_rotation" prompt for the new one.
	provider := func(_ context.Context, h domain.PassphraseHint) ([]byte, error) {
		if h.Reason == "kek_rotation" {
			return nil, errors.New("provider unavailable")
		}
		return []byte("pw"), nil
	}

	s, err := store.OpenStore(ctx, drv,
		store.WithPassphrase(provider),
		store.WithAutoUnlock(),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}

	descBefore, err := descriptor.Read(ctx, drv)
	if err != nil {
		t.Fatalf("read descriptor before rotation: %v", err)
	}

	rotErr := s.RotateKEK(ctx)

	// 1. The provider's real failure must surface (callProvider wraps
	//    any provider error with ErrPassphraseProvider).
	if !errors.Is(rotErr, errs.ErrPassphraseProvider) {
		t.Errorf("RotateKEK: want ErrPassphraseProvider, got %v", rotErr)
	}
	// 2. It must NOT be masked as ErrPassphraseRequired — that is the
	//    exact regression: an unchecked provider error degrades into
	//    WrapDEK's empty-passphrase refusal.
	if errors.Is(rotErr, errs.ErrPassphraseRequired) {
		t.Errorf("RotateKEK: provider error masked as ErrPassphraseRequired: %v", rotErr)
	}

	// 3. No descriptor rewrite — a failed rotation leaves on-disk crypto
	//    state untouched (holds with or without the fix; documents the
	//    no-lockout invariant).
	descAfter, err := descriptor.Read(ctx, drv)
	if err != nil {
		t.Fatalf("read descriptor after rotation: %v", err)
	}
	if descAfter.Sequence != descBefore.Sequence {
		t.Errorf("descriptor Sequence changed on failed rotation: before %d, after %d",
			descBefore.Sequence, descAfter.Sequence)
	}

	// 4. The owner can still open with the original passphrase.
	if _, err := store.OpenStore(ctx, drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Errorf("original passphrase rejected after failed rotation: %v", err)
	}
}
