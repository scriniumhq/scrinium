package recoverykit_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"scrinium.dev/engine/store/internal/recoverykit"
	"scrinium.dev/errs"
)

// validKit returns a Kit with every field populated. Used as the
// happy-path baseline for round-trip and as the source of one-
// field mutations.
func validKit() recoverykit.Kit {
	return recoverykit.Kit{
		StoreID:      "550e8400-e29b-41d4-a716-446655440000",
		CreatedAt:    time.Date(2026, 3, 25, 14, 0, 0, 0, time.UTC),
		Algorithm:    "argon2id",
		Salt:         []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f},
		Time:         1,
		Memory:       65536,
		Threads:      4,
		EncryptedDEK: bytes.Repeat([]byte{0x42}, 60),
	}
}

func TestEncodeDecode_RoundTrip(t *testing.T) {
	src := validKit()
	data, err := recoverykit.Encode(src)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := recoverykit.Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if got.StoreID != src.StoreID {
		t.Errorf("StoreID: got %q, want %q", got.StoreID, src.StoreID)
	}
	if !got.CreatedAt.Equal(src.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, src.CreatedAt)
	}
	if got.Algorithm != src.Algorithm {
		t.Errorf("Algorithm: got %q, want %q", got.Algorithm, src.Algorithm)
	}
	if !bytes.Equal(got.Salt, src.Salt) {
		t.Errorf("Salt: got %x, want %x", got.Salt, src.Salt)
	}
	if got.Time != src.Time {
		t.Errorf("Time: got %d, want %d", got.Time, src.Time)
	}
	if got.Memory != src.Memory {
		t.Errorf("Memory: got %d, want %d", got.Memory, src.Memory)
	}
	if got.Threads != src.Threads {
		t.Errorf("Threads: got %d, want %d", got.Threads, src.Threads)
	}
	if !bytes.Equal(got.EncryptedDEK, src.EncryptedDEK) {
		t.Errorf("EncryptedDEK: got %x, want %x", got.EncryptedDEK, src.EncryptedDEK)
	}
}

func TestEncode_DeterministicForSameInput(t *testing.T) {
	src := validKit()
	a, _ := recoverykit.Encode(src)
	b, _ := recoverykit.Encode(src)
	if !bytes.Equal(a, b) {
		t.Fatal("Encode should be deterministic")
	}
}

func TestEncode_HeaderAndCreatedAtComment(t *testing.T) {
	data, err := recoverykit.Encode(validKit())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte(recoverykit.Header+"\n")) {
		t.Errorf("output should start with %q, got %q",
			recoverykit.Header, string(data[:50]))
	}
	if !bytes.Contains(data, []byte("# Generated: 2026-03-25T14:00:00Z\n")) {
		t.Error("output should contain Generated comment in UTC RFC3339")
	}
}

func TestEncode_FormatsLayout(t *testing.T) {
	data, err := recoverykit.Encode(validKit())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"[STORE]",
		"StoreID    = 550e8400-e29b-41d4-a716-446655440000",
		"[KDF]",
		"Algo    = argon2id",
		"Time    = 1",
		"Memory  = 65536",
		"Threads = 4",
		"[KEY]",
		"[CHECKSUM]",
		"Hash = sha256-",
	}
	for _, w := range want {
		if !bytes.Contains(data, []byte(w)) {
			t.Errorf("output missing %q", w)
		}
	}
}

// --- Validation: refuse to encode incomplete kits ---

func TestEncode_RejectsIncompleteKit(t *testing.T) {
	cases := []struct {
		name string
		f    func(k *recoverykit.Kit)
	}{
		{"empty StoreID", func(k *recoverykit.Kit) { k.StoreID = "" }},
		{"zero CreatedAt", func(k *recoverykit.Kit) { k.CreatedAt = time.Time{} }},
		{"empty Algorithm", func(k *recoverykit.Kit) { k.Algorithm = "" }},
		{"empty Salt", func(k *recoverykit.Kit) { k.Salt = nil }},
		{"zero Time", func(k *recoverykit.Kit) { k.Time = 0 }},
		{"zero Memory", func(k *recoverykit.Kit) { k.Memory = 0 }},
		{"zero Threads", func(k *recoverykit.Kit) { k.Threads = 0 }},
		{"empty DEK", func(k *recoverykit.Kit) { k.EncryptedDEK = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := validKit()
			tc.f(&k)
			if _, err := recoverykit.Encode(k); err == nil {
				t.Fatal("expected error on incomplete kit")
			}
		})
	}
}

// --- Decode: tamper detection through checksum ---

func TestDecode_DetectsBodyTamper(t *testing.T) {
	data, _ := recoverykit.Encode(validKit())

	// Find the StoreID line and corrupt one character. Choose a
	// non-base64-area mutation so the structural pass would
	// happily accept it; only the checksum stops us.
	idx := bytes.Index(data, []byte("StoreID    = 550e8400"))
	if idx < 0 {
		t.Fatal("test setup: cannot locate StoreID line")
	}
	tampered := append([]byte{}, data...)
	tampered[idx+len("StoreID    = ")] = 'X'

	_, err := recoverykit.Decode(tampered)
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("expected ErrRecoveryKitCorrupted, got %v", err)
	}
}

func TestDecode_DetectsChecksumTamper(t *testing.T) {
	data, _ := recoverykit.Encode(validKit())
	// Flip a byte inside the checksum hex.
	idx := bytes.Index(data, []byte("Hash = sha256-"))
	if idx < 0 {
		t.Fatal("test setup: cannot locate Hash line")
	}
	tampered := append([]byte{}, data...)
	pos := idx + len("Hash = sha256-")
	if tampered[pos] == '0' {
		tampered[pos] = '1'
	} else {
		tampered[pos] = '0'
	}

	_, err := recoverykit.Decode(tampered)
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("expected ErrRecoveryKitCorrupted, got %v", err)
	}
}

// --- Decode: structural rejections ---

func TestDecode_RejectsMissingHeader(t *testing.T) {
	bad := []byte("[STORE]\nStoreID = x\n[CHECKSUM]\nHash = sha256-deadbeef\n")
	_, err := recoverykit.Decode(bad)
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("expected ErrRecoveryKitCorrupted, got %v", err)
	}
}

func TestDecode_RejectsMissingChecksumSection(t *testing.T) {
	body := strings.Join([]string{
		recoverykit.Header,
		"# Generated: 2026-03-25T14:00:00Z",
		"",
		"[STORE]",
		"StoreID = x",
	}, "\n") + "\n"
	_, err := recoverykit.Decode([]byte(body))
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("expected ErrRecoveryKitCorrupted, got %v", err)
	}
}

func TestDecode_RejectsBadKeyEqualsValue(t *testing.T) {
	// Kit with a malformed line in [STORE]. Use Encode to get a
	// real prefix, then splice in the bad line before the
	// checksum section.
	good, _ := recoverykit.Encode(validKit())
	idx := bytes.Index(good, []byte("\n[CHECKSUM]\n"))
	if idx < 0 {
		t.Fatal("test setup")
	}
	corrupted := append([]byte{}, good[:idx]...)
	corrupted = append(corrupted, []byte("MalformedLineWithoutEquals\n")...)
	corrupted = append(corrupted, good[idx:]...)

	_, err := recoverykit.Decode(corrupted)
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("expected ErrRecoveryKitCorrupted, got %v", err)
	}
}

func TestDecode_RejectsUnknownSection(t *testing.T) {
	good, _ := recoverykit.Encode(validKit())
	idx := bytes.Index(good, []byte("\n[CHECKSUM]\n"))
	if idx < 0 {
		t.Fatal("test setup")
	}
	corrupted := append([]byte{}, good[:idx]...)
	corrupted = append(corrupted, []byte("\n[BOGUS]\nKey = Val\n")...)
	corrupted = append(corrupted, good[idx:]...)
	_, err := recoverykit.Decode(corrupted)
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("expected ErrRecoveryKitCorrupted, got %v", err)
	}
}

func TestDecode_RejectsBadSaltBase64(t *testing.T) {
	good, _ := recoverykit.Encode(validKit())
	corrupted := bytes.Replace(good,
		[]byte("Salt    = "+"AAECAwQFBgcICQoLDA0ODw=="), // base64 of 16-byte salt above
		[]byte("Salt    = "+"!!!not-base64!!!"),
		1)
	if bytes.Equal(corrupted, good) {
		t.Skip("base64 of test salt has changed; replace marker")
	}
	_, err := recoverykit.Decode(corrupted)
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("expected ErrRecoveryKitCorrupted, got %v", err)
	}
}

func TestDecode_RejectsUnsupportedHashAlgo(t *testing.T) {
	good, _ := recoverykit.Encode(validKit())
	corrupted := bytes.Replace(good,
		[]byte("sha256-"),
		[]byte("md5----"),
		1)
	_, err := recoverykit.Decode(corrupted)
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("expected ErrRecoveryKitCorrupted, got %v", err)
	}
}

func TestDecode_RejectsEmptyInput(t *testing.T) {
	_, err := recoverykit.Decode(nil)
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("expected ErrRecoveryKitCorrupted, got %v", err)
	}
	_, err = recoverykit.Decode([]byte{})
	if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
		t.Fatalf("expected ErrRecoveryKitCorrupted, got %v", err)
	}
}

// TestDecode_RejectsMalformedWithoutPanic guards against the
// shadowed-checksumIdx footgun in the previous Decode: a malformed
// input without a "\n[CHECKSUM]\n" sentinel must surface as
// ErrRecoveryKitCorrupted rather than panicking on a body slice
// computed from a -1 index. The current Decode is linear; this
// test locks the contract so a future "simplification" cannot
// reintroduce the old shadow-based recovery branch by accident.
func TestDecode_RejectsMalformedWithoutPanic(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{"only_header", []byte(recoverykit.Header + "\n")},
		{"header_and_store_section_without_checksum",
			[]byte(recoverykit.Header + "\n\n[STORE]\nStoreID = abc\n")},
		{"random_text_no_sentinel",
			[]byte("not a recovery kit at all\nrandom lines\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Decode panicked on %q: %v", tc.name, r)
				}
			}()
			_, err := recoverykit.Decode(tc.in)
			if !errors.Is(err, errs.ErrRecoveryKitCorrupted) {
				t.Fatalf("Decode(%q): err = %v, want errs.ErrRecoveryKitCorrupted",
					tc.name, err)
			}
		})
	}
}
