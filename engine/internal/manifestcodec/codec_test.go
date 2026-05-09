package manifestcodec_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/errs"
	"github.com/rkurbatov/scrinium/engine/internal/manifestcodec"
	"github.com/rkurbatov/scrinium/engine/internal/testutil/storefx"
	"github.com/rkurbatov/scrinium/testutil/manifestfx"
)

var (
	sampleManifest  = manifestfx.Sample
	newHashRegistry = storefx.Hashes
)

// --- EncodeFile / DecodeFile round-trip ---

func TestEncodeDecodeFile_RoundTrip(t *testing.T) {
	src := sampleManifest()
	bs, err := manifestcodec.EncodeFile(src, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatalf("EncodeFile: %v", err)
	}

	got, err := manifestcodec.DecodeFile(bs)
	if err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	if got.Type != src.Type {
		t.Errorf("Type: got %q, want %q", got.Type, src.Type)
	}
	if got.Namespace != src.Namespace {
		t.Errorf("Namespace: got %q, want %q", got.Namespace, src.Namespace)
	}
	if got.BlobRef != src.BlobRef {
		t.Errorf("BlobRef: got %q, want %q", got.BlobRef, src.BlobRef)
	}
	if got.OriginalSize != src.OriginalSize {
		t.Errorf("OriginalSize: got %d, want %d", got.OriginalSize, src.OriginalSize)
	}
	if !got.CreatedAt.Equal(src.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", got.CreatedAt, src.CreatedAt)
	}
}

func TestEncodeFile_StartsWithMagic(t *testing.T) {
	bs, err := manifestcodec.EncodeFile(sampleManifest(),
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x00, 'S', 'C', '1', 0x00}
	if !bytes.HasPrefix(bs, want) {
		t.Errorf("bad header: got %x, want prefix %x", bs[:5], want)
	}
}

func TestEncodeFile_DeterministicOrder(t *testing.T) {
	// Encode twice; output must be byte-identical.
	bs1, _ := manifestcodec.EncodeFile(sampleManifest(),
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	bs2, _ := manifestcodec.EncodeFile(sampleManifest(),
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if !bytes.Equal(bs1, bs2) {
		t.Errorf("non-deterministic encoding:\n  bs1 = %s\n  bs2 = %s",
			hex.EncodeToString(bs1), hex.EncodeToString(bs2))
	}
}

func TestEncodeFile_KeysAreAlphabetical(t *testing.T) {
	bs, _ := manifestcodec.EncodeFile(sampleManifest(),
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	body := string(bs[5:])
	// Spot-check ordering: blob_ref < content_hash < created_at
	// layout_header < namespace < original_size < pipeline
	// schema_version < session_id < type.
	idx := func(k string) int { return strings.Index(body, `"`+k+`"`) }
	order := []string{"blob_ref", "content_hash", "created_at",
		"layout_header", "namespace", "original_size", "pipeline",
		"schema_version", "session_id", "type"}
	prev := -1
	for _, k := range order {
		i := idx(k)
		if i < 0 {
			t.Errorf("key %q missing in body", k)
			continue
		}
		if i < prev {
			t.Errorf("key %q out of order: appears at %d, previous was %d", k, i, prev)
		}
		prev = i
	}
}

func TestEncodeFile_NoWhitespace(t *testing.T) {
	bs, _ := manifestcodec.EncodeFile(sampleManifest(),
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	body := bs[5:]
	if bytes.Contains(body, []byte{'\n'}) {
		t.Error("body contains newline (must be compact)")
	}
	// Find ":" outside string values; they must not be followed by
	// space. We can't fully parse JSON here, so we approximate by
	// checking ": " never appears outside a quoted timestamp etc.
	// A loose check: count ": " occurrences vs "T..Z" timestamps.
	if bytes.Contains(body, []byte(`, `)) {
		t.Error("body contains ', ' separator (must be compact)")
	}
}

// --- Optional fields ---

func TestEncodeFile_OmitsRetentionWhenZero(t *testing.T) {
	bs, _ := manifestcodec.EncodeFile(sampleManifest(),
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if bytes.Contains(bs, []byte("retention_until")) {
		t.Error("retention_until included even though zero")
	}
}

func TestEncodeDecodeFile_RetentionRoundTrip(t *testing.T) {
	m := sampleManifest()
	m.RetentionUntil = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	bs, _ := manifestcodec.EncodeFile(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	got, err := manifestcodec.DecodeFile(bs)
	if err != nil {
		t.Fatal(err)
	}
	if !got.RetentionUntil.Equal(m.RetentionUntil) {
		t.Errorf("retention round-trip: got %v, want %v",
			got.RetentionUntil, m.RetentionUntil)
	}
}

func TestEncodeDecodeFile_InlineBlob(t *testing.T) {
	m := sampleManifest()
	m.LayoutHeader.BlobStorage = "Inline"
	m.InlineBlob = []byte{0xde, 0xad, 0xbe, 0xef}
	bs, _ := manifestcodec.EncodeFile(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	got, err := manifestcodec.DecodeFile(bs)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.InlineBlob, m.InlineBlob) {
		t.Errorf("InlineBlob: got %x, want %x", got.InlineBlob, m.InlineBlob)
	}
}

func TestEncodeDecodeFile_PipelineRoundTrip(t *testing.T) {
	m := sampleManifest()
	m.Pipeline = []domain.PipelineStage{
		{Algorithm: "zstd", Hash: "sha256-" + strings.Repeat("c", 64)},
		{Algorithm: "aes-gcm", Hash: "sha256-" + strings.Repeat("d", 64),
			IV: []byte{0x01, 0x02, 0x03}},
	}
	bs, _ := manifestcodec.EncodeFile(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	got, err := manifestcodec.DecodeFile(bs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Pipeline) != 2 {
		t.Fatalf("Pipeline length: got %d, want 2", len(got.Pipeline))
	}
	if got.Pipeline[0].Algorithm != "zstd" {
		t.Errorf("stage 0 algo: got %q", got.Pipeline[0].Algorithm)
	}
	if !bytes.Equal(got.Pipeline[1].IV, m.Pipeline[1].IV) {
		t.Errorf("stage 1 IV: got %x, want %x",
			got.Pipeline[1].IV, m.Pipeline[1].IV)
	}
}

// --- Error paths ---

func TestEncodeFile_RejectsBinaryEncoding(t *testing.T) {
	_, err := manifestcodec.EncodeFile(sampleManifest(),
		domain.ManifestEncodingBinary, domain.ManifestCryptoPlain)
	if !errors.Is(err, errs.ErrUnsupportedEncoding) {
		t.Fatalf("expected errs.ErrUnsupportedEncoding, got %v", err)
	}
}

func TestEncodeFile_RejectsNonPlainCrypto(t *testing.T) {
	_, err := manifestcodec.EncodeFile(sampleManifest(),
		domain.ManifestEncodingJSON, domain.ManifestCryptoEnvelope)
	if !errors.Is(err, errs.ErrUnsupportedCrypto) {
		t.Fatalf("expected errs.ErrUnsupportedCrypto, got %v", err)
	}
}

func TestDecodeFile_RejectsTooShort(t *testing.T) {
	_, err := manifestcodec.DecodeFile([]byte{0x00, 'S'})
	if err == nil {
		t.Fatal("expected error on truncated file")
	}
}

func TestDecodeFile_RejectsUnknownMagic(t *testing.T) {
	bad := []byte{'X', 'Y', 'Z', '!', 0x00, '{', '}'}
	_, err := manifestcodec.DecodeFile(bad)
	if err == nil {
		t.Fatal("expected error on unknown magic")
	}
}

func TestDecodeFile_RejectsBinaryMagic(t *testing.T) {
	bad := []byte{0x00, 'S', 'C', '2', 0x00, '{', '}'}
	_, err := manifestcodec.DecodeFile(bad)
	if !errors.Is(err, errs.ErrUnsupportedEncoding) {
		t.Fatalf("expected errs.ErrUnsupportedEncoding, got %v", err)
	}
}

func TestDecodeFile_RejectsEnvelopeFlag(t *testing.T) {
	// Well-formed Envelope header (default-key, no KeyID) followed
	// by bytes that would be the encrypted body. M2.3.1 parses the
	// header and refuses any non-Plain crypto at the body decode
	// step, before touching the body.
	bad := []byte{0x00, 'S', 'C', '1', 0x02, 0x00, '{', '}'}
	_, err := manifestcodec.DecodeFile(bad)
	if !errors.Is(err, errs.ErrUnsupportedCrypto) {
		t.Fatalf("expected errs.ErrUnsupportedCrypto, got %v", err)
	}
}

func TestDecodeFile_RejectsUnknownBodyField(t *testing.T) {
	// Build a normal file then append a body with an extra key.
	// Easier: hand-craft.
	body := `{"blob_ref":"sha256-x","layout_header":{"blob_storage":"Target"},"namespace":"","pipeline":[],"schema_version":1,"session_id":"","type":"blob","unknown_xyz":"oops","created_at":"2026-04-01T12:00:00Z"}`
	bs := append([]byte{0x00, 'S', 'C', '1', 0x00}, body...)
	_, err := manifestcodec.DecodeFile(bs)
	if err == nil {
		t.Fatal("expected error on unknown field")
	}
}

func TestDecodeFile_RejectsFutureSchemaVersion(t *testing.T) {
	body := `{"blob_ref":"sha256-x","layout_header":{"blob_storage":"Target"},"namespace":"","pipeline":[],"schema_version":99,"session_id":"","type":"blob","created_at":"2026-04-01T12:00:00Z"}`
	bs := append([]byte{0x00, 'S', 'C', '1', 0x00}, body...)
	_, err := manifestcodec.DecodeFile(bs)
	if !errors.Is(err, errs.ErrUnsupportedSchemaVersion) {
		t.Fatalf("expected errs.ErrUnsupportedSchemaVersion, got %v", err)
	}
}

// --- ComputeArtifactID + VerifyArtifactID ---

func TestComputeArtifactID_AssignsAndStable(t *testing.T) {
	registry := newHashRegistry()
	src := sampleManifest()

	id, bs, withID, err := manifestcodec.ComputeArtifactID(src, "sha256", registry,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if err != nil {
		t.Fatalf("ComputeArtifactID: %v", err)
	}
	if id == "" {
		t.Fatal("empty ArtifactID")
	}
	if !strings.HasPrefix(string(id), "sha256-") {
		t.Errorf("ArtifactID prefix: got %q", id)
	}
	if withID.ArtifactID != id {
		t.Errorf("withID.ArtifactID: got %q, want %q", withID.ArtifactID, id)
	}
	// VerifyArtifactID closes the loop.
	if err := manifestcodec.VerifyArtifactID(id, bs, registry); err != nil {
		t.Errorf("VerifyArtifactID: %v", err)
	}
}

func TestComputeArtifactID_DifferentManifestsDifferentIDs(t *testing.T) {
	registry := newHashRegistry()
	a := sampleManifest()
	b := sampleManifest()
	b.Namespace = "different"
	idA, _, _, _ := manifestcodec.ComputeArtifactID(a, "sha256", registry,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	idB, _, _, _ := manifestcodec.ComputeArtifactID(b, "sha256", registry,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if idA == idB {
		t.Errorf("different manifests produced same ArtifactID: %q", idA)
	}
}

func TestVerifyArtifactID_DetectsTampering(t *testing.T) {
	registry := newHashRegistry()
	id, bs, _, err := manifestcodec.ComputeArtifactID(sampleManifest(),
		"sha256", registry,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the body.
	tampered := make([]byte, len(bs))
	copy(tampered, bs)
	if tampered[20] != 'X' {
		tampered[20] = 'X'
	} else {
		tampered[20] = 'Y'
	}
	err = manifestcodec.VerifyArtifactID(id, tampered, registry)
	if !errors.Is(err, errs.ErrCorruptedManifest) {
		t.Errorf("expected errs.ErrCorruptedManifest, got %v", err)
	}
}

// TestEncodeFile_ArtifactIDNotInBody is an invariant: per docs §7.4
// the ArtifactID is the hash of the full file bytes, so it cannot
// appear inside the body — that would create a chicken-and-egg
// dependency. The Manifest.ArtifactID field is in-memory only and
// must NEVER round-trip to disk through EncodeFile. If this test
// fails, codec.go's jsonBody started leaking the field.
func TestEncodeFile_ArtifactIDNotInBody(t *testing.T) {
	m := sampleManifest()
	m.ArtifactID = domain.ArtifactID("sha256-deadbeef")
	bs, err := manifestcodec.EncodeFile(m, domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bs, []byte("artifact_id")) {
		t.Error("artifact_id key leaked into manifest body")
	}
	if bytes.Contains(bs, []byte("deadbeef")) {
		t.Error("ArtifactID value leaked into manifest body")
	}

	// And the round-trip clears it on the way back.
	got, err := manifestcodec.DecodeFile(bs)
	if err != nil {
		t.Fatal(err)
	}
	if got.ArtifactID != "" {
		t.Errorf("DecodeFile populated ArtifactID from body: got %q, want empty",
			got.ArtifactID)
	}
}

// --- Manifest size limit (domain.MaxManifestSize) ---

func TestEncodeFile_ManifestTooLarge(t *testing.T) {
	// Inflate Metadata to a JSON string of MaxManifestSize bytes.
	// The body always adds the other fields on top, so the final
	// file is guaranteed to exceed the limit. The payload uses
	// only 'x' bytes — no JSON escaping inflates the count.
	huge := make([]byte, domain.MaxManifestSize)
	huge[0] = '"'
	for i := 1; i < len(huge)-1; i++ {
		huge[i] = 'x'
	}
	huge[len(huge)-1] = '"'

	m := sampleManifest()
	m.Metadata = huge

	_, err := manifestcodec.EncodeFile(m,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if !errors.Is(err, errs.ErrManifestTooLarge) {
		t.Fatalf("EncodeFile: got %v, want errs.ErrManifestTooLarge", err)
	}
}

func TestEncodeFile_ManifestUnderLimit_OK(t *testing.T) {
	// Boundary positive check: a manifest comfortably under the
	// limit must still encode. Half the limit leaves plenty of
	// room for the other body fields.
	huge := make([]byte, domain.MaxManifestSize/2)
	huge[0] = '"'
	for i := 1; i < len(huge)-1; i++ {
		huge[i] = 'x'
	}
	huge[len(huge)-1] = '"'

	m := sampleManifest()
	m.Metadata = huge

	bs, err := manifestcodec.EncodeFile(m,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatalf("EncodeFile: %v", err)
	}
	if len(bs) > domain.MaxManifestSize {
		t.Fatalf("encoded size %d exceeds limit %d",
			len(bs), domain.MaxManifestSize)
	}
}
