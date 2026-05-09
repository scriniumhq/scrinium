package manifestcodec_test

import (
	"bytes"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/internal/manifestcodec"
	"scrinium.dev/testutil/manifestfx"
)

// FuzzDecodeFile probes DecodeFile against arbitrary byte input.
// The contract: a panic is always a bug; any return other than
// (Manifest, error) is a bug. Errors are expected and not failures.
//
// Seed corpus mixes minimal valid headers with deliberately damaged
// shapes (truncated, wrong magic, wrong crypto flag, malformed
// JSON body) so the fuzzer starts from "almost valid" and mutates
// outward. The valid samples come from EncodeFile; relying on the
// real encoder for seeds keeps the corpus aligned with what the
// codec actually produces.
func FuzzDecodeFile(f *testing.F) {
	// Seed: a real, valid encoded manifest.
	valid, err := manifestcodec.EncodeFile(
		manifestfx.Sample(),
		domain.ManifestEncodingJSON,
		domain.ManifestCryptoPlain,
	)
	if err != nil {
		f.Fatalf("seed encode: %v", err)
	}
	f.Add(valid)

	// Seed: empty input — boundary on the length check.
	f.Add([]byte{})
	// Seed: 4 bytes of magic, no crypto flag — boundary on the
	// length check (DecodeFile requires at least 5).
	f.Add([]byte{0x00, 'S', 'C', '1'})
	// Seed: valid magic + crypto flag, empty body — exercises the
	// JSON parser on an empty buffer.
	f.Add([]byte{0x00, 'S', 'C', '1', 0x00})
	// Seed: binary magic — should hit ErrUnsupportedEncoding, not
	// crash.
	f.Add([]byte{0x00, 'S', 'C', '2', 0x00})
	// Seed: unknown magic.
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x00})
	// Seed: valid header, body is "{".
	f.Add([]byte{0x00, 'S', 'C', '1', 0x00, '{'})
	// Seed: valid header, body is the empty JSON object.
	f.Add([]byte{0x00, 'S', 'C', '1', 0x00, '{', '}'})
	// Seed: valid header, crypto flag is non-zero (MetadataOnly).
	f.Add([]byte{0x00, 'S', 'C', '1', 0x01})
	// Seed: valid encoded manifest with one byte flipped at the
	// crypto-flag position — exercises the crypto check on what
	// was otherwise a valid file.
	flipped := append([]byte(nil), valid...)
	flipped[4] = 0xFF
	f.Add(flipped)

	f.Fuzz(func(t *testing.T, data []byte) {
		// Property 1: never panic. Reaching the end of the closure
		// is success; a panic is the failure mode go test -fuzz
		// catches automatically.
		m, err := manifestcodec.DecodeFile(data)
		if err != nil {
			// Expected for the vast majority of inputs.
			return
		}

		// Property 2: round-trip stability. If Decode succeeded,
		// re-encoding must produce a byte sequence that Decode
		// also accepts and that yields the same in-memory
		// representation. The output bytes do not have to equal
		// the input — the input may have whitespace or field
		// order the canonical encoder normalises away.
		reencoded, err := manifestcodec.EncodeFile(m,
			domain.ManifestEncodingJSON,
			domain.ManifestCryptoPlain)
		if err != nil {
			// Encoding what Decode accepted should not fail. If
			// it does, the codec is asymmetric — Decode is more
			// permissive than Encode, and that delta is a real
			// bug worth surfacing.
			t.Fatalf("re-encode failed for input that decoded cleanly: input=%x err=%v", data, err)
		}
		m2, err := manifestcodec.DecodeFile(reencoded)
		if err != nil {
			t.Fatalf("re-decoded re-encoded bytes failed: input=%x reencoded=%x err=%v",
				data, reencoded, err)
		}
		if !manifestEquivalent(m, m2) {
			t.Errorf("round-trip changed the manifest:\n  decode1=%+v\n  decode2=%+v",
				m, m2)
		}
	})
}

// manifestEquivalent compares two manifests for structural
// equality after a codec round-trip. Avoids reflect.DeepEqual
// because nil vs empty slices/maps that JSON normalises away
// would create false negatives.
//
// ArtifactID is NOT compared: per the codec contract, it is not
// serialised — both decoded values will be the zero string,
// regardless of the original input.
func manifestEquivalent(a, b domain.Manifest) bool {
	if a.Type != b.Type ||
		a.Namespace != b.Namespace ||
		a.SessionID != b.SessionID ||
		a.ContentHash != b.ContentHash ||
		a.OriginalSize != b.OriginalSize ||
		a.BlobRef != b.BlobRef ||
		a.ExternalURI != b.ExternalURI ||
		a.KeyID != b.KeyID {
		return false
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return false
	}
	if !a.RetentionUntil.Equal(b.RetentionUntil) {
		return false
	}
	if a.LayoutHeader != b.LayoutHeader {
		return false
	}
	if a.SystemFlags != b.SystemFlags {
		return false
	}
	if !bytes.Equal(a.InlineBlob, b.InlineBlob) {
		return false
	}
	if len(a.Pipeline) != len(b.Pipeline) {
		return false
	}
	for i := range a.Pipeline {
		if !pipelineStageEquivalent(a.Pipeline[i], b.Pipeline[i]) {
			return false
		}
	}
	// Metadata is json.RawMessage. Trim whitespace before comparing —
	// canonical encoding may compact it.
	if !bytes.Equal(bytes.TrimSpace(a.Metadata), bytes.TrimSpace(b.Metadata)) {
		return false
	}
	return true
}

func pipelineStageEquivalent(a, b domain.PipelineStage) bool {
	if a.Algorithm != b.Algorithm || a.Hash != b.Hash {
		return false
	}
	return bytes.Equal(a.IV, b.IV)
}
