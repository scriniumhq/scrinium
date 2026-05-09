package manifestcodec_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/errs"
	"github.com/rkurbatov/scrinium/engine/internal/manifestcodec"
)

// --- writeHeader: layout per §7.1 ---

func TestWriteHeader_PlainIs5Bytes(t *testing.T) {
	got, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCryptoPlain,
	})
	if err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	want := []byte{0x00, 'S', 'C', '1', 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("Plain header: got %x, want %x", got, want)
	}
}

func TestWriteHeader_DefaultEncodingMapsToJSON(t *testing.T) {
	got, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
		// Encoding zero — must default to JSON.
		Crypto: domain.ManifestCryptoPlain,
	})
	if err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	if !bytes.HasPrefix(got, manifestcodec.MagicJSON) {
		t.Errorf("zero-Encoding should default to JSON magic, got %x", got[:4])
	}
}

func TestWriteHeader_EnvelopeDefaultKeyIDIs6Bytes(t *testing.T) {
	got, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCryptoEnvelope,
	})
	if err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	want := []byte{0x00, 'S', 'C', '1', manifestcodec.CryptoEnvelopeFlag, 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("Envelope default-KeyID header: got %x, want %x", got, want)
	}
}

func TestWriteHeader_MetadataOnlyWithKeyID(t *testing.T) {
	got, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCryptoMetadataOnly,
		KeyID:    "tenant-a",
	})
	if err != nil {
		t.Fatalf("WriteHeader: %v", err)
	}
	want := []byte{
		0x00, 'S', 'C', '1',
		manifestcodec.CryptoMetadataOnlyFlag,
		8, // KeyID length
		't', 'e', 'n', 'a', 'n', 't', '-', 'a',
	}
	if !bytes.Equal(got, want) {
		t.Errorf("MetadataOnly+KeyID header: got %x, want %x", got, want)
	}
}

func TestWriteHeader_RejectsKeyIDOnPlain(t *testing.T) {
	_, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCryptoPlain,
		KeyID:    "anything",
	})
	if err == nil {
		t.Fatal("KeyID on Plain should be rejected")
	}
}

func TestWriteHeader_RejectsTooLongKeyID(t *testing.T) {
	long := strings.Repeat("a", 256)
	_, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCryptoEnvelope,
		KeyID:    long,
	})
	if err == nil {
		t.Fatal("KeyID > MaxKeyIDLength should be rejected")
	}
}

func TestWriteHeader_AcceptsMaximumKeyID(t *testing.T) {
	max := strings.Repeat("k", domain.MaxKeyIDLength)
	got, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCryptoEnvelope,
		KeyID:    max,
	})
	if err != nil {
		t.Fatalf("WriteHeader at MaxKeyIDLength: %v", err)
	}
	wantLen := 4 + 1 + 1 + domain.MaxKeyIDLength
	if len(got) != wantLen {
		t.Errorf("len: got %d, want %d", len(got), wantLen)
	}
}

func TestWriteHeader_RejectsInvalidUTF8(t *testing.T) {
	// Lone continuation byte — not a valid UTF-8 start byte.
	bad := string([]byte{0x80})
	_, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCryptoEnvelope,
		KeyID:    bad,
	})
	if err == nil {
		t.Fatal("invalid-UTF-8 KeyID should be rejected")
	}
}

func TestWriteHeader_RejectsBinaryEncoding(t *testing.T) {
	_, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingBinary,
		Crypto:   domain.ManifestCryptoPlain,
	})
	if !errors.Is(err, errs.ErrUnsupportedEncoding) {
		t.Fatalf("expected ErrUnsupportedEncoding, got %v", err)
	}
}

func TestWriteHeader_RejectsUnknownCrypto(t *testing.T) {
	_, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCrypto("alien"),
	})
	if !errors.Is(err, errs.ErrUnsupportedCrypto) {
		t.Fatalf("expected ErrUnsupportedCrypto, got %v", err)
	}
}

// --- readHeader ---

func TestReadHeader_PlainRoundTrip(t *testing.T) {
	src := manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCryptoPlain,
	}
	raw, _ := manifestcodec.WriteHeader(src)

	got, off, err := manifestcodec.ReadHeader(raw)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if got != src {
		t.Errorf("header: got %+v, want %+v", got, src)
	}
	if off != 5 {
		t.Errorf("body offset: got %d, want 5", off)
	}
}

func TestReadHeader_MetadataOnlyRoundTrip(t *testing.T) {
	src := manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCryptoMetadataOnly,
		KeyID:    "tenant-a",
	}
	raw, _ := manifestcodec.WriteHeader(src)

	got, off, err := manifestcodec.ReadHeader(raw)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if got != src {
		t.Errorf("header: got %+v, want %+v", got, src)
	}
	if off != 6+len(src.KeyID) {
		t.Errorf("body offset: got %d, want %d", off, 6+len(src.KeyID))
	}
}

func TestReadHeader_EnvelopeDefaultKeyIDRoundTrip(t *testing.T) {
	src := manifestcodec.FileHeader{
		Encoding: domain.ManifestEncodingJSON,
		Crypto:   domain.ManifestCryptoEnvelope,
		// KeyID empty — default-key path.
	}
	raw, _ := manifestcodec.WriteHeader(src)

	got, off, err := manifestcodec.ReadHeader(raw)
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if got != src {
		t.Errorf("header: got %+v, want %+v", got, src)
	}
	if off != 6 {
		t.Errorf("body offset: got %d, want 6", off)
	}
}

func TestReadHeader_RejectsTooShort(t *testing.T) {
	for _, n := range []int{0, 1, 4} {
		buf := make([]byte, n)
		_, _, err := manifestcodec.ReadHeader(buf)
		if err == nil {
			t.Errorf("len=%d: expected truncation error", n)
		}
	}
}

func TestReadHeader_RejectsUnknownMagic(t *testing.T) {
	_, _, err := manifestcodec.ReadHeader([]byte{0xff, 0xff, 0xff, 0xff, 0x00})
	if err == nil {
		t.Fatal("unknown magic must error")
	}
}

func TestReadHeader_BinaryMagicSurfacesUnsupportedEncoding(t *testing.T) {
	bin := []byte{0x00, 'S', 'C', '2', 0x00}
	_, _, err := manifestcodec.ReadHeader(bin)
	if !errors.Is(err, errs.ErrUnsupportedEncoding) {
		t.Fatalf("expected ErrUnsupportedEncoding, got %v", err)
	}
}

func TestReadHeader_RejectsUnknownCryptoFlag(t *testing.T) {
	bad := []byte{0x00, 'S', 'C', '1', 0x99}
	_, _, err := manifestcodec.ReadHeader(bad)
	if !errors.Is(err, errs.ErrUnsupportedCrypto) {
		t.Fatalf("expected ErrUnsupportedCrypto, got %v", err)
	}
}

func TestReadHeader_RejectsTruncatedBeforeKeyIDLength(t *testing.T) {
	// Encrypted flag but only 5 bytes (no length byte).
	bad := []byte{0x00, 'S', 'C', '1', manifestcodec.CryptoEnvelopeFlag}
	_, _, err := manifestcodec.ReadHeader(bad)
	if err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestReadHeader_RejectsTruncatedInsideKeyID(t *testing.T) {
	// Length byte says 8, but only 4 KeyID bytes follow.
	bad := []byte{
		0x00, 'S', 'C', '1', manifestcodec.CryptoEnvelopeFlag,
		8,
		'a', 'b', 'c', 'd',
	}
	_, _, err := manifestcodec.ReadHeader(bad)
	if err == nil {
		t.Fatal("expected truncation error")
	}
}

func TestReadHeader_RejectsInvalidUTF8KeyID(t *testing.T) {
	bad := []byte{
		0x00, 'S', 'C', '1', manifestcodec.CryptoEnvelopeFlag,
		3,
		0x80, 0x80, 0x80, // continuation bytes, no start byte
	}
	_, _, err := manifestcodec.ReadHeader(bad)
	if err == nil {
		t.Fatal("expected UTF-8 validation error")
	}
}

// --- crypto flag mapping ---

func TestCryptoFlag_AllValues(t *testing.T) {
	cases := []struct {
		in   domain.ManifestCrypto
		want byte
	}{
		{"", manifestcodec.CryptoPlainFlag},
		{domain.ManifestCryptoPlain, manifestcodec.CryptoPlainFlag},
		{domain.ManifestCryptoMetadataOnly, manifestcodec.CryptoMetadataOnlyFlag},
		{domain.ManifestCryptoEnvelope, manifestcodec.CryptoEnvelopeFlag},
	}
	for _, tc := range cases {
		got, err := manifestcodec.CryptoFlag(tc.in)
		if err != nil {
			t.Errorf("%q: error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got 0x%02x, want 0x%02x", tc.in, got, tc.want)
		}
	}
}

func TestCryptoFromFlag_AllValues(t *testing.T) {
	cases := []struct {
		in   byte
		want domain.ManifestCrypto
	}{
		{manifestcodec.CryptoPlainFlag, domain.ManifestCryptoPlain},
		{manifestcodec.CryptoMetadataOnlyFlag, domain.ManifestCryptoMetadataOnly},
		{manifestcodec.CryptoEnvelopeFlag, domain.ManifestCryptoEnvelope},
	}
	for _, tc := range cases {
		got, err := manifestcodec.CryptoFromFlag(tc.in)
		if err != nil {
			t.Errorf("0x%02x: error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("0x%02x: got %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := manifestcodec.CryptoFromFlag(0x99); !errors.Is(err, errs.ErrUnsupportedCrypto) {
		t.Errorf("unknown flag should give ErrUnsupportedCrypto, got %v", err)
	}
}

// --- Fuzz: header round-trip ---

func FuzzWriteReadHeader(f *testing.F) {
	// Seed corpus: a few interesting cases.
	f.Add(uint8(0), uint8(0), "")                       // Plain
	f.Add(uint8(0), uint8(1), "tenant-a")               // MetadataOnly + KeyID
	f.Add(uint8(0), uint8(2), "")                       // Envelope default key
	f.Add(uint8(0), uint8(2), strings.Repeat("k", 255)) // Envelope max KeyID

	f.Fuzz(func(t *testing.T, encMagicVariant, cryptoVariant uint8, keyID string) {
		// Map random uint8s to legal enum values; out-of-range
		// inputs should also be tested as "expected error" cases.
		var encoding domain.ManifestEncoding
		switch encMagicVariant % 2 {
		case 0:
			encoding = domain.ManifestEncodingJSON
		case 1:
			encoding = domain.ManifestEncodingJSON // also test default
		}

		var crypto domain.ManifestCrypto
		switch cryptoVariant {
		case 0:
			crypto = domain.ManifestCryptoPlain
		case 1:
			crypto = domain.ManifestCryptoMetadataOnly
		case 2:
			crypto = domain.ManifestCryptoEnvelope
		default:
			// Out-of-range — writeHeader should refuse.
			_, err := manifestcodec.WriteHeader(manifestcodec.FileHeader{
				Encoding: encoding,
				Crypto:   domain.ManifestCrypto(fmt.Sprintf("crypto-%d", cryptoVariant)),
				KeyID:    keyID,
			})
			if err == nil {
				t.Fatal("invalid crypto must error")
			}
			return
		}

		// Plain rejects KeyID — only test KeyID under encrypted modes.
		if crypto == domain.ManifestCryptoPlain {
			keyID = ""
		}

		src := manifestcodec.FileHeader{
			Encoding: encoding,
			Crypto:   crypto,
			KeyID:    keyID,
		}
		raw, err := manifestcodec.WriteHeader(src)
		if err != nil {
			// Expected for KeyID > 255 or invalid UTF-8.
			return
		}

		got, _, err := manifestcodec.ReadHeader(raw)
		if err != nil {
			t.Fatalf("ReadHeader on round-trip output: %v\nbytes: %x", err, raw)
		}
		if got != src {
			t.Errorf("round-trip mismatch:\n got %+v\nwant %+v", got, src)
		}
	})
}

// --- Fuzz: random bytes must not panic readHeader ---

func FuzzReadHeader_NoPanic(f *testing.F) {
	// Seed with valid headers and short fragments.
	f.Add([]byte{})
	f.Add([]byte{0x00})
	f.Add([]byte{0x00, 'S', 'C', '1', 0x00})
	f.Add([]byte{0x00, 'S', 'C', '1', 0x02, 0x05, 'a', 'b', 'c'})

	f.Fuzz(func(t *testing.T, data []byte) {
		// Must either parse cleanly or return an error — never panic.
		_, _, _ = manifestcodec.ReadHeader(data)
	})
}
