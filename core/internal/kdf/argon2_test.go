package kdf_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/rkurbatov/scrinium/core/internal/kdf"
)

func deriveDefault(t *testing.T, passphrase string) []byte {
	t.Helper()
	salt, err := kdf.NewSalt()
	if err != nil {
		t.Fatal(err)
	}
	p := kdf.Default()
	return kdf.Derive([]byte(passphrase), salt, p.Time, p.Memory, p.Threads)
}

func TestDerive_OutputLength(t *testing.T) {
	kek := deriveDefault(t, "correct horse battery staple")
	if len(kek) != kdf.KEKLen {
		t.Fatalf("KEK length: got %d, want %d", len(kek), kdf.KEKLen)
	}
}

func TestDerive_DeterministicForSameInputs(t *testing.T) {
	salt, _ := kdf.NewSalt()
	p := kdf.Default()
	pass := []byte("correct horse battery staple")
	kek1 := kdf.Derive(pass, salt, p.Time, p.Memory, p.Threads)
	kek2 := kdf.Derive(pass, salt, p.Time, p.Memory, p.Threads)
	if !bytes.Equal(kek1, kek2) {
		t.Fatal("Derive is not deterministic for identical inputs")
	}
}

func TestDerive_DifferentSaltsProduceDifferentKeys(t *testing.T) {
	p := kdf.Default()
	salt1, _ := kdf.NewSalt()
	salt2, _ := kdf.NewSalt()
	pass := []byte("correct horse battery staple")
	k1 := kdf.Derive(pass, salt1, p.Time, p.Memory, p.Threads)
	k2 := kdf.Derive(pass, salt2, p.Time, p.Memory, p.Threads)
	if bytes.Equal(k1, k2) {
		t.Fatal("different salts produced identical KEK")
	}
}

func TestDerive_DifferentPassphrasesProduceDifferentKeys(t *testing.T) {
	p := kdf.Default()
	salt, _ := kdf.NewSalt()
	a := kdf.Derive([]byte("passphrase A"), salt, p.Time, p.Memory, p.Threads)
	b := kdf.Derive([]byte("passphrase B"), salt, p.Time, p.Memory, p.Threads)
	if bytes.Equal(a, b) {
		t.Fatal("different passphrases produced identical KEK")
	}
}

// TestDerive_KnownVector locks in cross-version stability of the
// derivation. If golang.org/x/crypto/argon2 ever changes the
// output for the same inputs, this test catches it before any
// real Store fails to unlock.
func TestDerive_KnownVector(t *testing.T) {
	fixedSalt := []byte{
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	}
	got := kdf.Derive(
		[]byte("scrinium-test-vector"),
		fixedSalt,
		1,     // Time
		65536, // Memory KiB
		4,     // Threads
	)

	// Phase 1 (first commit): non-zero check.
	allZero := true
	for _, b := range got {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("KEK is all zeros — argon2 broken")
	}

	// Locked-in vector. Regenerating this requires bumping
	// Descriptor.SchemaVersion: any existing Store with the old
	// derivation would fail to unlock.
	want, err := hex.DecodeString(
		"714628d809f94735527f686c9ade0df1ae33a0e80b6c9d9b168f99fc0afe9946",
	)
	if err != nil {
		t.Fatalf("decode want: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("KEK mismatch:\n got: %x\nwant: %x", got, want)
	}
}
