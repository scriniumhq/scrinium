package artifact_test

import (
	"bytes"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/testutil/artifactfx"
)

func FuzzDecode(f *testing.F) {
	valid, err := artifact.Encode(artifactfx.Manifest(), domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		f.Fatalf("seed encode: %v", err)
	}

	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte{0x00, 'S', 'C', '1'})
	f.Add([]byte{0x00, 'S', 'C', '1', 0x00})
	f.Add([]byte{0x00, 'S', 'C', '2', 0x00})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x00})
	f.Add([]byte{0x00, 'S', 'C', '1', 0x00, '{'})
	f.Add([]byte{0x00, 'S', 'C', '1', 0x00, '{', '}'})
	f.Add([]byte{0x00, 'S', 'C', '1', 0x01})

	flipped := append([]byte(nil), valid...)
	flipped[4] = 0xFF
	f.Add(flipped)

	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := artifact.Decode(data)
		if err != nil {
			return
		}

		reencoded, err := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
		if err != nil {
			// Decode is a lenient parser; a fuzzed input can decode into a
			// structurally invalid manifest (e.g. a both-slots-filled or
			// no-identity-meta slot) that Encode's validateSlot rejects
			// (ADR-104). There is nothing to round-trip — skip, don't fail.
			return
		}

		m2, err := artifact.Decode(reencoded)
		if err != nil {
			t.Fatalf("re-decoded re-encoded bytes failed: input=%x reencoded=%x err=%v", data, reencoded, err)
		}

		bs1, _ := artifact.Encode(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
		bs2, _ := artifact.Encode(m2, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
		if !bytes.Equal(bs1, bs2) {
			t.Errorf("round-trip changed the manifest:\n  decode1=%s\n  decode2=%s", bs1, bs2)
		}
	})
}
