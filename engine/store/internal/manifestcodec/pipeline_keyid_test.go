package manifestcodec_test

import (
	"bytes"
	"strings"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/store/internal/manifestcodec"
)

// TestEncodeDecodeFile_PipelineKeyIDRoundTrip exercises the
// KeyID-in-stage round-trip introduced for KeyResolver-backed
// crypto plugins. The field rides alongside Algorithm/Hash/IV
// in the JSON body and must survive an encode/decode cycle
// verbatim.
func TestEncodeDecodeFile_PipelineKeyIDRoundTrip(t *testing.T) {
	m := sampleManifest()
	m.Pipeline = []domain.PipelineStage{
		{Algorithm: "zstd", Hash: "sha256-" + strings.Repeat("e", 64)},
		{
			Algorithm: "aes-gcm",
			Hash:      "sha256-" + strings.Repeat("f", 64),
			IV:        []byte{0x10, 0x20, 0x30},
			KeyID:     "tenant-42",
		},
	}
	bs, err := manifestcodec.EncodeFile(m,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatalf("EncodeFile: %v", err)
	}
	got, err := manifestcodec.DecodeFile(bs)
	if err != nil {
		t.Fatalf("DecodeFile: %v", err)
	}
	if len(got.Pipeline) != 2 {
		t.Fatalf("Pipeline length: got %d, want 2", len(got.Pipeline))
	}
	if got.Pipeline[0].KeyID != "" {
		t.Errorf("non-crypto stage: KeyID got %q, want empty",
			got.Pipeline[0].KeyID)
	}
	if got.Pipeline[1].KeyID != "tenant-42" {
		t.Errorf("crypto stage KeyID: got %q, want %q",
			got.Pipeline[1].KeyID, "tenant-42")
	}
}

// TestEncodeFile_OmitsKeyIDWhenEmpty confirms the omitempty tag
// keeps the on-disk format clean for non-crypto and legacy
// pinned-DEK stages: no key_id field appears in their JSON
// rendering.
func TestEncodeFile_OmitsKeyIDWhenEmpty(t *testing.T) {
	m := sampleManifest()
	m.Pipeline = []domain.PipelineStage{
		{Algorithm: "zstd", Hash: "sha256-" + strings.Repeat("e", 64)},
	}
	bs, err := manifestcodec.EncodeFile(m,
		domain.ManifestEncodingJSON, domain.ManifestCryptoPlain)
	if err != nil {
		t.Fatalf("EncodeFile: %v", err)
	}
	if bytes.Contains(bs, []byte(`"key_id"`)) {
		t.Errorf("key_id present in body despite empty KeyID:\n%s", bs[5:])
	}
}
