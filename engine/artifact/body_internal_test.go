package artifact

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/errs"
	"scrinium.dev/testutil/manifestfx"
)

// localManifest creates test manifest breaking import cycle
func localManifest() domain.Manifest {
	m := manifestfx.Sample()
	m.Pipeline = []domain.PipelineStage{}
	m.Ext = json.RawMessage(`{"k":"ext-value"}`)
	m.Usr = json.RawMessage(`{"u":"usr-value"}`)
	m.InlineBlob = []byte("inline-secret-bytes")
	return m
}

func TestMarshalBodyJSON_KeysAreAlphabetical(t *testing.T) {
	bs, err := marshalBodyJSON(localManifest())
	if err != nil {
		t.Fatal(err)
	}
	body := string(bs)
	idx := func(k string) int { return strings.Index(body, `"`+k+`"`) }

	order := []string{
		"ext",
		"inline_blob",
		"sys",
		"blob_ref",
		"content_hash",
		"created_at",
		"layout_header",
		"namespace",
		"original_size",
		"pipeline",
		"schema_version",
		"session_id",
		"type",
		"usr",
	}

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

func TestMarshalBodyJSON_NoWhitespace(t *testing.T) {
	bs, _ := marshalBodyJSON(localManifest())
	if bytes.Contains(bs, []byte{'\n'}) || bytes.Contains(bs, []byte{'\t'}) {
		t.Error("body contains newline or tab (must be compact)")
	}
	if bytes.Contains(bs, []byte(`, `)) {
		t.Error("body contains ', ' separator (must be compact)")
	}
}

func TestMarshalBodyJSON_OmitsRetentionWhenZero(t *testing.T) {
	bs, _ := marshalBodyJSON(localManifest())
	if bytes.Contains(bs, []byte("retention_until")) {
		t.Error("retention_until included even though zero")
	}
}

func TestMarshalBodyJSON_OmitsKeyIDWhenEmpty(t *testing.T) {
	m := localManifest()
	m.Pipeline = []domain.PipelineStage{
		{Algorithm: "zstd", Hash: "sha256-" + strings.Repeat("e", 64)},
	}

	bs, _ := marshalBodyJSON(m)
	if bytes.Contains(bs, []byte(`"key_id"`)) {
		t.Errorf("key_id present in body despite empty KeyID:\n%s", bs)
	}
}

func TestMarshalBodyJSON_ArtifactIDNotInBody(t *testing.T) {
	m := localManifest()
	m.ArtifactID = domain.ArtifactID("sha256-deadbeef")

	bs, _ := marshalBodyJSON(m)
	if bytes.Contains(bs, []byte("artifact_id")) || bytes.Contains(bs, []byte("deadbeef")) {
		t.Error("ArtifactID leaked into manifest body")
	}

	got, _ := unmarshalBodyJSON(bs)
	if got.ArtifactID != "" {
		t.Errorf("unmarshal populated ArtifactID from body: got %q, want empty", got.ArtifactID)
	}
}

func TestUnmarshalBodyJSON_RejectsUnknownField(t *testing.T) {
	body := []byte(`{"sys":{"blob_ref":"sha256-x","pipeline":[],"schema_version":1},"unknown_xyz":"oops"}`)
	_, err := unmarshalBodyJSON(body)
	if err == nil {
		t.Fatal("expected error on unknown field")
	}
}

func TestUnmarshalBodyJSON_RejectsFutureSchemaVersion(t *testing.T) {
	body := []byte(`{"sys":{"blob_ref":"sha256-x","pipeline":[],"schema_version":99}}`)
	_, err := unmarshalBodyJSON(body)
	if !errors.Is(err, errs.ErrUnsupportedSchemaVersion) {
		t.Fatalf("expected errs.ErrUnsupportedSchemaVersion, got %v", err)
	}
}
