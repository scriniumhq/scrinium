package artifact

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"scrinium.dev/config"
	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// --- writeHeader: byte layout (the on-disk contract) ---

func TestWriteHeader_PlainIs5Bytes(t *testing.T) {
	out, err := writeHeader(fileHeader{Crypto: config.ManifestCryptoPlain})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 5 {
		t.Fatalf("Plain header must be 5 bytes, got %d (%x)", len(out), out)
	}
	if !bytes.Equal(out[:4], magicJSON) {
		t.Errorf("magic: got %x, want %x", out[:4], magicJSON)
	}
	if out[4] != cryptoPlain {
		t.Errorf("crypto flag: got %#x, want %#x", out[4], cryptoPlain)
	}
}

func TestWriteHeader_DefaultEncodingMapsToJSON(t *testing.T) {
	out, err := writeHeader(fileHeader{Encoding: "", Crypto: config.ManifestCryptoPlain})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out[:4], magicJSON) {
		t.Errorf("empty encoding should map to JSON magic, got %x", out[:4])
	}
}

func TestWriteHeader_SealedWithKeyID(t *testing.T) {
	out, err := writeHeader(fileHeader{Crypto: config.ManifestCryptoSealed, KeyID: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	// magic(4) + flag(1) + keyIDLen(1) + "k1"(2) = 8
	if len(out) != 8 {
		t.Fatalf("got %d bytes, want 8 (%x)", len(out), out)
	}
	if out[4] != cryptoSealed {
		t.Errorf("flag: got %#x, want %#x", out[4], cryptoSealed)
	}
	if out[5] != 2 {
		t.Errorf("KeyID length byte: got %d, want 2", out[5])
	}
	if string(out[6:]) != "k1" {
		t.Errorf("KeyID bytes: got %q, want k1", out[6:])
	}
}

func TestWriteHeader_ParanoidEmptyKeyIDIs6Bytes(t *testing.T) {
	out, err := writeHeader(fileHeader{Crypto: config.ManifestCryptoParanoid})
	if err != nil {
		t.Fatal(err)
	}
	// magic(4)+flag(1)+keyIDLen(1)=6, KeyID empty.
	if len(out) != 6 {
		t.Fatalf("got %d bytes, want 6 (%x)", len(out), out)
	}
	if out[5] != 0 {
		t.Errorf("empty KeyID length byte must be 0, got %d", out[5])
	}
}

// --- writeHeader: error paths ---

func TestWriteHeader_RejectsKeyIDOnPlain(t *testing.T) {
	if _, err := writeHeader(fileHeader{Crypto: config.ManifestCryptoPlain, KeyID: "nope"}); err == nil {
		t.Fatal("KeyID under Plain must be rejected")
	}
}

func TestWriteHeader_RejectsTooLongKeyID(t *testing.T) {
	long := strings.Repeat("x", domain.MaxKeyIDLength+1)
	_, err := writeHeader(fileHeader{Crypto: config.ManifestCryptoSealed, KeyID: long})
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig, got %v", err)
	}
}

func TestWriteHeader_AcceptsMaximumKeyID(t *testing.T) {
	max := strings.Repeat("x", domain.MaxKeyIDLength)
	if _, err := writeHeader(fileHeader{Crypto: config.ManifestCryptoSealed, KeyID: max}); err != nil {
		t.Fatalf("KeyID at the limit must be accepted: %v", err)
	}
}

func TestWriteHeader_RejectsInvalidUTF8KeyID(t *testing.T) {
	_, err := writeHeader(fileHeader{Crypto: config.ManifestCryptoSealed, KeyID: string([]byte{0xff, 0xfe})})
	if !errors.Is(err, errs.ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig on invalid UTF-8, got %v", err)
	}
}

func TestWriteHeader_RejectsBinaryEncoding(t *testing.T) {
	_, err := writeHeader(fileHeader{Encoding: config.ManifestEncodingBinary, Crypto: config.ManifestCryptoPlain})
	if !errors.Is(err, errs.ErrUnsupportedEncoding) {
		t.Fatalf("want ErrUnsupportedEncoding, got %v", err)
	}
}

func TestWriteHeader_RejectsUnknownCrypto(t *testing.T) {
	_, err := writeHeader(fileHeader{Crypto: config.ManifestCrypto("Quantum")})
	if !errors.Is(err, errs.ErrUnsupportedCrypto) {
		t.Fatalf("want ErrUnsupportedCrypto, got %v", err)
	}
}

// --- readHeader: round-trips ---

func TestReadHeader_PlainRoundTrip(t *testing.T) {
	out, _ := writeHeader(fileHeader{Crypto: config.ManifestCryptoPlain})
	h, off, err := readHeader(out)
	if err != nil {
		t.Fatal(err)
	}
	if h.Crypto != config.ManifestCryptoPlain || off != 5 || h.KeyID != "" {
		t.Errorf("got crypto=%q off=%d keyID=%q", h.Crypto, off, h.KeyID)
	}
}

func TestReadHeader_SealedRoundTrip(t *testing.T) {
	out, _ := writeHeader(fileHeader{Crypto: config.ManifestCryptoSealed, KeyID: "key-42"})
	h, off, err := readHeader(out)
	if err != nil {
		t.Fatal(err)
	}
	if h.Crypto != config.ManifestCryptoSealed || h.KeyID != "key-42" || off != len(out) {
		t.Errorf("got crypto=%q keyID=%q off=%d", h.Crypto, h.KeyID, off)
	}
}

func TestReadHeader_ParanoidEmptyKeyIDRoundTrip(t *testing.T) {
	out, _ := writeHeader(fileHeader{Crypto: config.ManifestCryptoParanoid})
	h, off, err := readHeader(out)
	if err != nil {
		t.Fatal(err)
	}
	if h.Crypto != config.ManifestCryptoParanoid || off != 6 {
		t.Errorf("got crypto=%q off=%d", h.Crypto, off)
	}
}

// --- readHeader: error paths ---

func TestReadHeader_RejectsTooShort(t *testing.T) {
	if _, _, err := readHeader([]byte{0x00, 'S', 'C'}); err == nil {
		t.Fatal("expected error on <5 byte input")
	}
}

func TestReadHeader_RejectsUnknownMagic(t *testing.T) {
	if _, _, err := readHeader([]byte{'X', 'X', 'X', 'X', cryptoPlain}); err == nil {
		t.Fatal("expected error on unknown magic")
	}
}

func TestReadHeader_BinaryMagicSurfacesUnsupportedEncoding(t *testing.T) {
	data := append(slices.Clone(magicBinary), cryptoPlain)
	_, _, err := readHeader(data)
	if !errors.Is(err, errs.ErrUnsupportedEncoding) {
		t.Fatalf("want ErrUnsupportedEncoding, got %v", err)
	}
}

func TestReadHeader_RejectsUnknownCryptoFlag(t *testing.T) {
	data := append(slices.Clone(magicJSON), 0x09)
	_, _, err := readHeader(data)
	if !errors.Is(err, errs.ErrUnsupportedCrypto) {
		t.Fatalf("want ErrUnsupportedCrypto, got %v", err)
	}
}

func TestReadHeader_RejectsTruncatedBeforeKeyIDLength(t *testing.T) {
	// magic + sealed flag, but no KeyID-length byte.
	data := append(slices.Clone(magicJSON), cryptoSealed)
	if _, _, err := readHeader(data); err == nil {
		t.Fatal("expected truncation error before KeyID length")
	}
}

func TestReadHeader_RejectsTruncatedInsideKeyID(t *testing.T) {
	// claims 5-byte KeyID but provides 2.
	data := append(slices.Clone(magicJSON), cryptoSealed, 0x05, 'a', 'b')
	if _, _, err := readHeader(data); err == nil {
		t.Fatal("expected truncation error inside KeyID")
	}
}

func TestReadHeader_RejectsInvalidUTF8KeyID(t *testing.T) {
	data := append(slices.Clone(magicJSON), cryptoSealed, 0x02, 0xff, 0xfe)
	if _, _, err := readHeader(data); err == nil {
		t.Fatal("expected error on invalid UTF-8 KeyID")
	}
}

// --- flag tables ---

func TestCryptoFlagRoundTrip_AllValues(t *testing.T) {
	for _, c := range []config.ManifestCrypto{
		config.ManifestCryptoPlain, config.ManifestCryptoSealed, config.ManifestCryptoParanoid,
	} {
		flag, err := cryptoFlag(c)
		if err != nil {
			t.Fatalf("cryptoFlag(%q): %v", c, err)
		}
		back, err := cryptoFromFlag(flag)
		if err != nil {
			t.Fatalf("cryptoFromFlag(%#x): %v", flag, err)
		}
		if back != c {
			t.Errorf("round-trip: %q -> %#x -> %q", c, flag, back)
		}
	}
}

// --- fuzz: write/read round-trip never panics and preserves fields ---

func FuzzWriteReadHeader(f *testing.F) {
	f.Add(uint8(cryptoPlain), "")
	f.Add(uint8(cryptoSealed), "k1")
	f.Add(uint8(cryptoParanoid), "")
	f.Fuzz(func(t *testing.T, flag uint8, keyID string) {
		var c config.ManifestCrypto
		switch flag % 3 {
		case 0:
			c, keyID = config.ManifestCryptoPlain, ""
		case 1:
			c = config.ManifestCryptoSealed
		default:
			c = config.ManifestCryptoParanoid
		}
		out, err := writeHeader(fileHeader{Crypto: c, KeyID: keyID})
		if err != nil {
			return // rejected inputs (too long / invalid UTF-8) are fine
		}
		h, _, err := readHeader(out)
		if err != nil {
			t.Fatalf("readHeader rejected what writeHeader produced: %v", err)
		}
		if h.Crypto != c || h.KeyID != keyID {
			t.Fatalf("round-trip mismatch: in (%q,%q) out (%q,%q)", c, keyID, h.Crypto, h.KeyID)
		}
	})
}

func FuzzReadHeader_NoPanic(f *testing.F) {
	f.Add([]byte{0x00, 'S', 'C', '1', 0x00})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _ = readHeader(data) // must never panic
	})
}
