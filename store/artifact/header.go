package artifact

// header.go — the manifest file-format header (magic + crypto flag +
// optional KeyID) per docs/2 Internals/07 §7.1. The byte values here are
// the on-disk contract: changing any of them requires a migration. The
// body codec lives in body.go and the encrypted modes in crypto.go.

import (
	"bytes"
	"fmt"
	"unicode/utf8"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
)

// File-format magic bytes (§7.1).
var (
	magicJSON   = []byte{0x00, 'S', 'C', '1'}
	magicBinary = []byte{0x00, 'S', 'C', '2'}
)

// Crypto flags (§7.1). Byte values are stable across the ADR-55 rename so
// old manifests on disk read transparently.
const (
	cryptoPlain    = 0x00
	cryptoSealed   = 0x01
	cryptoParanoid = 0x02
)

// SchemaVersion is the only on-disk version this package writes and reads.
// A higher version in a file surfaces as the forward-compat sentinel; a
// lower one becomes readable once migrations ship.
const SchemaVersion = 1

// fileHeader is the parsed/produced header of a manifest file. Internal to
// the package: callers pass the equivalent fields through Encode arguments
// and read them back from the decoded Manifest.
type fileHeader struct {
	Encoding domain.ManifestEncoding
	Crypto   domain.ManifestCrypto
	KeyID    string
}

// writeHeader serialises h to its on-disk representation (§7.1):
//
//   - 4 bytes magic
//   - 1 byte crypto flag
//   - if crypto != Plain: 1 byte KeyID length L (0..255), then L bytes KeyID
//
// Returns ErrUnsupportedEncoding / ErrUnsupportedCrypto for unknown modes,
// and a wrapped ErrInvalidConfig when the KeyID is over MaxKeyIDLength or
// not valid UTF-8. A KeyID under Plain is an error, not silently dropped:
// the Plain layout is fixed at 5 bytes.
func writeHeader(h fileHeader) ([]byte, error) {
	magic, err := encodingMagic(h.Encoding)
	if err != nil {
		return nil, err
	}
	flag, err := cryptoFlag(h.Crypto)
	if err != nil {
		return nil, err
	}

	if flag == cryptoPlain {
		if h.KeyID != "" {
			return nil, fmt.Errorf("artifact: KeyID set under Plain crypto")
		}
		out := make([]byte, 0, 5)
		out = append(out, magic...)
		out = append(out, flag)
		return out, nil
	}

	if len(h.KeyID) > domain.MaxKeyIDLength {
		return nil, fmt.Errorf("%w: KeyID is %d bytes, limit is %d",
			errs.ErrInvalidConfig, len(h.KeyID), domain.MaxKeyIDLength)
	}
	if !utf8.ValidString(h.KeyID) {
		return nil, fmt.Errorf("%w: KeyID is not valid UTF-8", errs.ErrInvalidConfig)
	}

	out := make([]byte, 0, 6+len(h.KeyID))
	out = append(out, magic...)
	out = append(out, flag)
	out = append(out, byte(len(h.KeyID)))
	out = append(out, []byte(h.KeyID)...)
	return out, nil
}

// readHeader parses the leading bytes of a manifest file and returns the
// header plus the offset where the body begins.
//
// Errors: ErrUnsupportedEncoding for the reserved binary magic (\x00SC2),
// ErrUnsupportedCrypto for an unknown crypto flag, and a plain parse error
// for missing-magic / truncated-header / invalid-UTF-8-KeyID. These are
// deliberately NOT ErrCorruptedManifest, which is reserved for an
// ArtifactID mismatch (a stronger statement); the caller decides how to
// surface a header-level malformation.
func readHeader(data []byte) (fileHeader, int, error) {
	if len(data) < 5 {
		return fileHeader{}, 0, fmt.Errorf("artifact: file too short (%d bytes)", len(data))
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

	if len(data) < 6 {
		return fileHeader{}, 0, fmt.Errorf("artifact: file truncated before KeyID length")
	}
	keyIDLen := int(data[5])
	headerEnd := 6 + keyIDLen
	if len(data) < headerEnd {
		return fileHeader{}, 0, fmt.Errorf("artifact: file truncated inside KeyID (need %d bytes, have %d)",
			headerEnd, len(data))
	}

	keyID := string(data[6:headerEnd])
	if !utf8.ValidString(keyID) {
		return fileHeader{}, 0, fmt.Errorf("artifact: KeyID is not valid UTF-8")
	}

	return fileHeader{Encoding: enc, Crypto: crypto, KeyID: keyID}, headerEnd, nil
}

// encodingMagic returns the 4-byte magic for an encoding; empty maps to
// JSON (the default). Binary is reserved but unsupported.
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

// magicEncoding is the inverse of encodingMagic.
func magicEncoding(magic []byte) (domain.ManifestEncoding, error) {
	switch {
	case bytes.Equal(magic, magicJSON):
		return domain.ManifestEncodingJSON, nil
	case bytes.Equal(magic, magicBinary):
		return "", errs.ErrUnsupportedEncoding
	default:
		return "", fmt.Errorf("artifact: unknown magic %x", magic)
	}
}

// cryptoFlag maps a ManifestCrypto value to its on-disk byte.
func cryptoFlag(c domain.ManifestCrypto) (byte, error) {
	switch c {
	case "", domain.ManifestCryptoPlain:
		return cryptoPlain, nil
	case domain.ManifestCryptoSealed:
		return cryptoSealed, nil
	case domain.ManifestCryptoParanoid:
		return cryptoParanoid, nil
	default:
		return 0, errs.ErrUnsupportedCrypto
	}
}

// cryptoFromFlag is the inverse of cryptoFlag.
func cryptoFromFlag(flag byte) (domain.ManifestCrypto, error) {
	switch flag {
	case cryptoPlain:
		return domain.ManifestCryptoPlain, nil
	case cryptoSealed:
		return domain.ManifestCryptoSealed, nil
	case cryptoParanoid:
		return domain.ManifestCryptoParanoid, nil
	default:
		return "", errs.ErrUnsupportedCrypto
	}
}
