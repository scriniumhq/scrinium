// Crypto administration: Unlock, SetPassphrase, RotateKEK,
// ExportRecoveryKit. Enumerable rejections (wrong/absent passphrase,
// already-encrypted, plain-store, locked-store) collapse into one table;
// the passphrase-provider hint Reason per operation is a second table; the
// state-changing happy paths and the no-lockout RotateKEK regression are
// kept as focused tests. Now in package storesuite (was the lone external
// storesuite_test).

package storesuite

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

// TestUnlock_LockedStoreSucceeds: a Locked encrypted store unlocks to
// Unlocked.
func TestUnlock_LockedStoreSucceeds(t *testing.T) {
	s, _ := storefx.InitEncryptedLocked(t, "hunter2")
	if err := s.Unlock(context.Background()); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if s.State() != domain.StateUnlocked {
		t.Errorf("State after Unlock: got %v, want Unlocked", s.State())
	}
}

// TestUnlock_IdempotentOnUnlocked: Unlock on an already-Unlocked store is a
// no-op that stays Unlocked — both for a Plain store (born unlocked, never
// prompts the nil provider) and an encrypted store after its first unlock.
func TestUnlock_IdempotentOnUnlocked(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) store.Store
	}{
		{"plain store born unlocked", func(t *testing.T) store.Store {
			s, _ := storefx.InitPlain(t)
			return s
		}},
		{"encrypted store after first unlock", func(t *testing.T) store.Store {
			s, _ := storefx.InitEncryptedLocked(t, "pw")
			if err := s.Unlock(context.Background()); err != nil {
				t.Fatalf("first Unlock: %v", err)
			}
			return s
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.setup(t)
			if err := s.Unlock(context.Background()); err != nil {
				t.Fatalf("redundant Unlock: %v", err)
			}
			if s.State() != domain.StateUnlocked {
				t.Errorf("State should stay Unlocked, got %v", s.State())
			}
		})
	}
}

// TestUnlock_WrongPassphrase: a wrong passphrase fails with
// ErrDecryptionFailed and leaves the store Locked.
func TestUnlock_WrongPassphrase(t *testing.T) {
	drv := driverfx.LocalFS(t)
	storefx.InitEncryptedOn(t, drv, "right")
	s, err := storefx.TryOpenOn(t, drv,
		store.WithPassphrase(storefx.StaticPP("wrong")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Unlock(context.Background()); !errors.Is(err, errs.ErrDecryptionFailed) {
		t.Fatalf("expected ErrDecryptionFailed, got %v", err)
	}
	if s.State() != domain.StateLocked {
		t.Errorf("State after failed Unlock: got %v, want Locked", s.State())
	}
}

// TestCryptoAdmin_Rejected: each crypto-admin operation refuses with the
// documented sentinel when its precondition is unmet.
func TestCryptoAdmin_Rejected(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T) error
		want error // nil = any non-nil error
	}{
		{"unlock without provider", func(t *testing.T) error {
			drv := driverfx.LocalFS(t)
			storefx.InitEncryptedOn(t, drv, "pw")
			s, err := storefx.TryOpenOn(t, drv)
			if err != nil {
				t.Fatal(err)
			}
			return s.Unlock(context.Background())
		}, errs.ErrPassphraseRequired},
		{"set passphrase on already-encrypted", func(t *testing.T) error {
			s, _ := storefx.InitEncryptedLocked(t, "pw")
			if err := s.Unlock(context.Background()); err != nil {
				t.Fatal(err)
			}
			return s.SetPassphrase(context.Background())
		}, errs.ErrPassphraseAlreadySet},
		{"set passphrase without provider", func(t *testing.T) error {
			s, _ := storefx.InitPlain(t)
			return s.SetPassphrase(context.Background())
		}, errs.ErrPassphraseRequired},
		{"rotate with wrong current passphrase", func(t *testing.T) error {
			drv := driverfx.LocalFS(t)
			storefx.InitEncryptedOn(t, drv, "right")
			s, err := storefx.TryOpenOn(t, drv,
				store.WithPassphrase(storefx.ScriptedPP("right", "wrong", "new")),
				store.WithAutoUnlock(),
			)
			if err != nil {
				t.Fatal(err)
			}
			return s.RotateKEK(context.Background())
		}, errs.ErrDecryptionFailed},
		{"rotate on plain store", func(t *testing.T) error {
			s, _ := storefx.InitPlain(t)
			return s.RotateKEK(context.Background())
		}, errs.ErrPassphraseRequired},
		{"export recovery kit on plain store", func(t *testing.T) error {
			s, _ := storefx.InitPlain(t)
			_, err := s.ExportRecoveryKit(context.Background())
			return err
		}, errs.ErrPassphraseRequired},
		{"export recovery kit on locked store", func(t *testing.T) error {
			s, _ := storefx.InitEncryptedLocked(t, "pw")
			_, err := s.ExportRecoveryKit(context.Background())
			return err
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(t)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("%s: expected an error", tc.name)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestCryptoAdmin_HintReasons: each operation calls the passphrase provider
// with the Reason a keychain integration can dispatch on — "unlock" for
// Unlock, "set_passphrase" for SetPassphrase, and the pair
// ["unlock","kek_rotation"] for RotateKEK (current then new).
func TestCryptoAdmin_HintReasons(t *testing.T) {
	cases := []struct {
		name        string
		run         func(t *testing.T) []domain.PassphraseHint
		wantReasons []string
	}{
		{"unlock", func(t *testing.T) []domain.PassphraseHint {
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
			return hints
		}, []string{"unlock"}},
		{"set_passphrase", func(t *testing.T) []domain.PassphraseHint {
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
			return hints
		}, []string{"set_passphrase"}},
		{"rotate_kek", func(t *testing.T) []domain.PassphraseHint {
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
			hints = hints[:0] // drop the AutoUnlock prompt
			if err := s.RotateKEK(context.Background()); err != nil {
				t.Fatal(err)
			}
			return hints
		}, []string{"unlock", "kek_rotation"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hints := tc.run(t)
			if len(hints) != len(tc.wantReasons) {
				t.Fatalf("%s: got %d provider calls, want %d", tc.name, len(hints), len(tc.wantReasons))
			}
			for i, want := range tc.wantReasons {
				if hints[i].Reason != want {
					t.Errorf("%s: hint[%d].Reason = %q, want %q", tc.name, i, hints[i].Reason, want)
				}
			}
		})
	}
}

// TestSetPassphrase_PlainStoreBecomesEncrypted: SetPassphrase on a Plain
// store wraps the DEK (DEKEncrypted, KDFParams, Sequence bumps to 2) and
// the store reopens under the new passphrase.
func TestSetPassphrase_PlainStoreBecomesEncrypted(t *testing.T) {
	drv := driverfx.LocalFS(t)
	idx := indexfx.Memory(t)
	if _, _, err := store.InitStore(context.Background(), drv,
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Fatal(err)
	}
	// Reopen with the SAME idx so Orphan Scan keeps known manifests.
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

	desc, _ := descriptor.Read(context.Background(), drv, storefx.Hashes())
	if !desc.DEKEncrypted {
		t.Error("descriptor.DEKEncrypted should be true after SetPassphrase")
	}
	if desc.KDFParams == nil {
		t.Error("KDFParams should be present")
	}
	if desc.Sequence != 2 {
		t.Errorf("Sequence: got %d, want 2", desc.Sequence)
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

// TestRotateKEK_RotatesPassphrase: a full rotation bumps the sequence,
// invalidates the old passphrase, and validates the new one.
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
	// Provider is called three times: AutoUnlock ("old-pw"), RotateKEK
	// current-pass verify ("old-pw"), RotateKEK new-pass ("new-pw").
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

	desc, _ := descriptor.Read(context.Background(), drv, storefx.Hashes())
	if desc.Sequence < 2 {
		t.Errorf("Sequence after rotation: got %d, want >= 2", desc.Sequence)
	}

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

// TestRotateKEK_NewPassphraseProviderError: when the provider errors on the
// new passphrase, RotateKEK surfaces ErrPassphraseProvider (not masked as
// ErrPassphraseRequired by WrapDEK's empty-passphrase guard), does not
// rewrite the descriptor, and leaves the original passphrase working.
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

	// Answers every "unlock" prompt (AutoUnlock + the current-pass verify)
	// with the real passphrase, but fails the "kek_rotation" prompt.
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

	descBefore, err := descriptor.Read(ctx, drv, storefx.Hashes())
	if err != nil {
		t.Fatalf("read descriptor before rotation: %v", err)
	}

	rotErr := s.RotateKEK(ctx)
	if !errors.Is(rotErr, errs.ErrPassphraseProvider) {
		t.Errorf("RotateKEK: want ErrPassphraseProvider, got %v", rotErr)
	}
	if errors.Is(rotErr, errs.ErrPassphraseRequired) {
		t.Errorf("RotateKEK: provider error masked as ErrPassphraseRequired: %v", rotErr)
	}

	descAfter, err := descriptor.Read(ctx, drv, storefx.Hashes())
	if err != nil {
		t.Fatalf("read descriptor after rotation: %v", err)
	}
	if descAfter.Sequence != descBefore.Sequence {
		t.Errorf("descriptor Sequence changed on failed rotation: before %d, after %d",
			descBefore.Sequence, descAfter.Sequence)
	}

	if _, err := store.OpenStore(ctx, drv,
		store.WithPassphrase(storefx.StaticPP("pw")),
		store.WithAutoUnlock(),
		store.WithStoreIndex(idx),
		store.WithHashRegistry(storefx.Hashes()),
	); err != nil {
		t.Errorf("original passphrase rejected after failed rotation: %v", err)
	}
}

// TestExportRecoveryKit_EncryptedStoreReturnsKit: an Unlocked encrypted
// store exports a decodable kit whose wrapped DEK matches the descriptor.
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

	parsed, err := recoverykit.Decode(kit)
	if err != nil {
		t.Fatalf("Decode kit: %v", err)
	}
	desc, _ := descriptor.Read(context.Background(), drv, storefx.Hashes())
	if !bytes.Equal(parsed.EncryptedDEK, desc.DEK) {
		t.Error("kit EncryptedDEK should match descriptor.DEK")
	}
}

// TestExportRecoveryKit_RegeneratesAfterRotation: the kit changes after
// RotateKEK and reflects the newly wrapped DEK.
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

	parsed, err := recoverykit.Decode(kitAfter)
	if err != nil {
		t.Fatalf("Decode kit: %v", err)
	}
	desc, _ := descriptor.Read(context.Background(), drv, storefx.Hashes())
	if !bytes.Equal(parsed.EncryptedDEK, desc.DEK) {
		t.Error("post-rotation kit must reflect new wrapped DEK")
	}
}
