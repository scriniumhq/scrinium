package manifestcodec

// header.go — file-format header (magic + crypto flag + optional
// KeyID) per docs/2. Internals/07 §7.1. Read/write helpers and the
// magic/crypto code-point tables live here; the body codec lives in
// body_json.go and the encrypted modes live in encrypted.go.

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
)

// File-format magic bytes from docs §7.1.
var (
	magicJSON   = []byte{0x00, 'S', 'C', '1'}
	magicBinary = []byte{0x00, 'S', 'C', '2'}
)

// Crypto flags from docs §7.1.
const (
	cryptoPlain        = 0x00
	cryptoMetadataOnly = 0x01
	cryptoEnvelope     = 0x02
)

// SchemaVersion is the only on-disk version this package writes
// and reads. Higher versions in a file return
// ErrUnsupportedSchemaVersion at the core sentinel; lower versions
// will be possible to read once we ship migrations.
const SchemaVersion = 1

// Forward-compat sentinels (errs.ErrUnsupportedEncoding,
// errs.ErrUnsupportedCrypto) live in the errs package — see
// errs/forward_compat.go.

// fileHeader describes the parsed/produced header fields of a
// manifest file. The struct is internal to this package; callers
// pass equivalent information through EncodeFile arguments and
// receive it back from DecodeFile via the populated Manifest.
type fileHeader struct {
	Encoding domain.ManifestEncoding
	Crypto   domain.ManifestCrypto
	KeyID    string
}

// writeHeader serialises h to its on-disk representation per §7.1.
//
// Layout:
//   - 4 bytes magic
//   - 1 byte crypto flag
//   - if crypto != Plain:
//     1 byte KeyID length L (0..255)
//     L bytes KeyID
//
// Returns ErrUnsupportedEncoding for unknown encodings,
// ErrUnsupportedCrypto for unknown crypto modes, and a wrapped
// error when the KeyID exceeds MaxKeyIDLength bytes.
func writeHeader(h fileHeader) ([]byte, error) {
	magic, err := encodingMagic(h.Encoding)
	if err != nil {
		return nil, err
	}
	flag, err := cryptoFlag(h.Crypto)
	if err != nil {
		return nil, err
	}

	// Plain: no KeyID at all, regardless of what the caller put
	// into h.KeyID. The §7.1 layout for Plain is fixed at 5 bytes
	// — silently ignoring a misplaced KeyID would be the wrong
	// kind of forgiving.
	if flag == cryptoPlain {
		if h.KeyID != "" {
			return nil, fmt.Errorf("manifestcodec: KeyID set under Plain crypto")
		}
		out := make([]byte, 0, 5)
		out = append(out, magic...)
		out = append(out, flag)
		return out, nil
	}

	// Encrypted modes: KeyID length byte plus KeyID bytes.
	if len(h.KeyID) > domain.MaxKeyIDLength {
		return nil, fmt.Errorf("manifestcodec: KeyID too long (%d bytes, max %d)",
			len(h.KeyID), domain.MaxKeyIDLength)
	}
	if !utf8.ValidString(h.KeyID) {
		return nil, fmt.Errorf("manifestcodec: KeyID not valid UTF-8")
	}

	out := make([]byte, 0, 6+len(h.KeyID))
	out = append(out, magic...)
	out = append(out, flag)
	out = append(out, byte(len(h.KeyID)))
	out = append(out, []byte(h.KeyID)...)
	return out, nil
}

// readHeader parses the leading bytes of a manifest file. Returns
// the parsed header and the offset where the body begins.
//
// Errors:
//   - ErrUnsupportedEncoding when magic is the binary variant
//     (\x00SC2) — we do not support it yet but the constant is
//     reserved.
//   - ErrUnsupportedCrypto when the crypto flag is outside the
//     three known values (0x00, 0x01, 0x02).
//   - A plain parse error for missing-magic, truncated-header,
//     or invalid-UTF-8 KeyID conditions. These are not
//     ErrCorruptedManifest because that sentinel is reserved
//     for ArtifactID-mismatch (a stronger statement); the
//     caller decides how to surface a header-level malformation.
func readHeader(data []byte) (fileHeader, int, error) {
	if len(data) < 5 {
		return fileHeader{}, 0, fmt.Errorf("manifestcodec: file too short (%d bytes)", len(data))
	}

	enc, err := magicEncoding(data[:4])
	if err != nil {
		return fileHeader{}, 0, err
	}

	crypto, err := cryptoFromFlag(data[4])
	if err != nil {
		return fileHeader{}, 0, err
	}

	if crypto == domain.ManifestCryptoPlain {
		return fileHeader{Encoding: enc, Crypto: crypto}, 5, nil
	}

	// Encrypted: read KeyID length and KeyID bytes.
	if len(data) < 6 {
		return fileHeader{}, 0, fmt.Errorf("manifestcodec: file truncated before KeyID length")
	}
	keyIDLen := int(data[5])
	headerEnd := 6 + keyIDLen
	if len(data) < headerEnd {
		return fileHeader{}, 0, fmt.Errorf("manifestcodec: file truncated inside KeyID (need %d bytes, have %d)",
			headerEnd, len(data))
	}

	keyID := string(data[6:headerEnd])
	if !utf8.ValidString(keyID) {
		return fileHeader{}, 0, fmt.Errorf("manifestcodec: KeyID is not valid UTF-8")
	}

	return fileHeader{Encoding: enc, Crypto: crypto, KeyID: keyID}, headerEnd, nil
}

// encodingMagic returns the 4-byte magic for the given encoding.
// Empty encoding maps to JSON (the default).
func encodingMagic(enc domain.ManifestEncoding) ([]byte, error) {
	switch enc {
	case "", domain.ManifestEncodingJSON:
		return magicJSON, nil
	case domain.ManifestEncodingBinary:
		return nil, errs.ErrUnsupportedEncoding
	default:
		return nil, errs.ErrUnsupportedEncoding
	}
}

// magicEncoding is the inverse of encodingMagic: matches the four
// leading bytes against known magics.
func magicEncoding(magic []byte) (domain.ManifestEncoding, error) {
	switch {
	case bytes.Equal(magic, magicJSON):
		return domain.ManifestEncodingJSON, nil
	case bytes.Equal(magic, magicBinary):
		return "", errs.ErrUnsupportedEncoding
	default:
		return "", fmt.Errorf("manifestcodec: unknown magic %x", magic)
	}
}

// cryptoFlag maps a domain ManifestCrypto value to its on-disk
// byte. Unknown values surface ErrUnsupportedCrypto.
func cryptoFlag(c domain.ManifestCrypto) (byte, error) {
	switch c {
	case "", domain.ManifestCryptoPlain:
		return cryptoPlain, nil
	case domain.ManifestCryptoMetadataOnly:
		return cryptoMetadataOnly, nil
	case domain.ManifestCryptoEnvelope:
		return cryptoEnvelope, nil
	default:
		return 0, errs.ErrUnsupportedCrypto
	}
}

// cryptoFromFlag is the inverse of cryptoFlag.
func cryptoFromFlag(flag byte) (domain.ManifestCrypto, error) {
	switch flag {
	case cryptoPlain:
		return domain.ManifestCryptoPlain, nil
	case cryptoMetadataOnly:
		return domain.ManifestCryptoMetadataOnly, nil
	case cryptoEnvelope:
		return domain.ManifestCryptoEnvelope, nil
	default:
		return "", errs.ErrUnsupportedCrypto
	}
}
