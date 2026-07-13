//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	scrinium "scrinium.dev"
	"scrinium.dev/config"
)

// End-to-end for the encrypted store lifecycle through the public facade
// only: seed an encrypting store from YAML, write an artifact, close,
// reopen from the same YAML, and read the bytes back — the whole
// init → put → close → reopen → get cycle an integrator drives, with
// the ciphertext surviving a full process-like restart (a fresh
// assembly on the same on-disk Location). A negative case confirms a
// wrong passphrase is refused rather than returning garbage.

// encryptedYAML is a store config with sealed manifest crypto and an
// inline (plain:) passphrase, so the test is hermetic — no env wiring.
func encryptedYAML(root, passphrase string) []byte {
	return []byte(fmt.Sprintf(`store:
  driver: file://%s
  policy:
    encryption:
      passphrase: plain:%s
      mode: sealed
`, root, passphrase))
}

func TestEncryptedLifecycle_SealedRoundtripAcrossReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	const passphrase = "correct horse battery staple"
	payload := []byte("sealed payload that must survive a reopen")

	// Seed the encrypting store and write one artifact.
	c, err := scrinium.LoadInitYAML(ctx, encryptedYAML(root, passphrase))
	if err != nil {
		t.Fatalf("LoadInitYAML (seed encrypted): %v", err)
	}
	if got := c.Store.Config().ManifestCrypto; got != config.ManifestCryptoSealed {
		t.Errorf("seeded ManifestCrypto = %q, want Sealed", got)
	}
	id, err := c.Store.Put(ctx, scrinium.Artifact{Payload: bytes.NewReader(payload)})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close after seed: %v", err)
	}

	// Reopen from the same YAML — a fresh assembly on the same Location,
	// standing in for a process restart — and read the artifact back.
	c, err = scrinium.LoadYAML(ctx, encryptedYAML(root, passphrase))
	if err != nil {
		t.Fatalf("LoadYAML (reopen encrypted): %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	rh, err := c.Store.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	got, err := io.ReadAll(rh)
	_ = rh.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decrypted payload mismatch:\n got %q\nwant %q", got, payload)
	}
}

// A reopen with the wrong passphrase must fail to unlock rather than
// silently returning corrupt plaintext. The exact error is
// unlock-related; the invariant under test is "refused, not garbage".
func TestEncryptedLifecycle_WrongPassphraseRefused(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	c, err := scrinium.LoadInitYAML(ctx, encryptedYAML(root, "the-real-secret"))
	if err != nil {
		t.Fatalf("LoadInitYAML (seed encrypted): %v", err)
	}
	if _, err := c.Store.Put(ctx, scrinium.Artifact{Payload: strings.NewReader("secret")}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopen with a different passphrase: must be refused at unlock.
	_, err = scrinium.LoadYAML(ctx, encryptedYAML(root, "wrong-secret"))
	if err == nil {
		t.Fatal("reopen with wrong passphrase: want an unlock failure, got nil")
	}
}
