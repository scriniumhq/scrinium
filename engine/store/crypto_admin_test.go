package store_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
	"scrinium.dev/engine/internal/testutil/storefx"
	"scrinium.dev/engine/plugins"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/store/internal/descriptor"
	"scrinium.dev/engine/store/internal/recoverykit"
	"scrinium.dev/internal/testutil/driverfx"
	"scrinium.dev/internal/testutil/indexfx"
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
	var hints []store.PassphraseHint
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
	var hints []store.PassphraseHint
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
	var hints []store.PassphraseHint
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

// --- KeyResolver promotion ---

func TestKeyResolverPromotion_OnUnlock(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Open without AutoUnlock — Locked, no DEK yet.
	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) != nil {
		t.Error("Locked Store should have no KeyResolver yet")
	}
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) == nil {
		t.Error("Unlock should populate the default KeyResolver")
	}
}

func TestKeyResolverPromotion_OnAutoUnlock(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) == nil {
		t.Error("AutoUnlock should populate default KeyResolver")
	}
}

func TestKeyResolverPromotion_RespectsCustomResolver(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}

	customDEK := bytes.Repeat([]byte{0xAB}, 32)
	custom := plugins.NewStaticKeyResolver(customDEK)

	s, err := store.OpenStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithKeyResolver(custom),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	got := store.StoreKeyResolver(s)
	if got == nil {
		t.Fatal("KeyResolver should not be nil")
	}
	// The custom resolver MUST have survived AutoUnlock — verify
	// by querying it (it returns customDEK, not s.dek).
	keys, err := got.GetKeys("")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !bytes.Equal(keys[0], customDEK) {
		t.Error("AutoUnlock overwrote the caller's custom KeyResolver")
	}
}

func TestKeyResolverPromotion_OnInitStore(t *testing.T) {
	drv := driverfx.LocalFS(t)
	s, _, err := store.InitStore(context.Background(), drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithStoreIndex(indexfx.Memory(t)),
		store.WithHashRegistry(storefx.Hashes()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.StoreKeyResolver(s) == nil {
		t.Error("InitStore on encrypted Store should populate default KeyResolver")
	}
}
