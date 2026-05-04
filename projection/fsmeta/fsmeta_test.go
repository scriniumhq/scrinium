package fsmeta_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/errs"
	"github.com/rkurbatov/scrinium/projection/fsmeta"
)

// --- ValidatePath ---

func TestValidatePath_Valid(t *testing.T) {
	cases := []string{
		"a",
		"a/b",
		"photos/2024/01/sunrise.jpg",
		"single-file.txt",
		"deeply/nested/path/with/many/segments/file.bin",
		"unicode/каталог/файл.txt",
		"with spaces/and-dashes/file.txt",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			if err := fsmeta.ValidatePath(p); err != nil {
				t.Errorf("expected valid, got %v", err)
			}
		})
	}
}

func TestValidatePath_Invalid(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"leading slash", "/photos/img.jpg"},
		{"NUL byte", "photos/img\x00.jpg"},
		{"trailing slash", "photos/"},
		{"double slash", "photos//img.jpg"},
		{"dot segment", "photos/./img.jpg"},
		{"dotdot segment", "photos/../img.jpg"},
		{"only dotdot", ".."},
		{"only dot", "."},
		{"trailing dotdot", "photos/.."},
		{"leading dotdot", "../etc/passwd"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := fsmeta.ValidatePath(tc.path)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.path)
			}
			if !errors.Is(err, errs.ErrInvalidPath) {
				t.Errorf("expected ErrInvalidPath, got %v", err)
			}
		})
	}
}

// --- ValidatePathWithReserved ---

func TestValidatePathWithReserved_Allowed(t *testing.T) {
	cases := []struct {
		path     string
		reserved string
	}{
		{"photos/img.jpg", "_scrinium"},
		{"a/b/c", "_scrinium"},
		{"_scrinium-suffix/file", "_scrinium"}, // not exact match
		{"sub/_scrinium/file", "_scrinium"},    // _scrinium not in root
		{"_scrinium", ""},                      // empty reserved disables check
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			if err := fsmeta.ValidatePathWithReserved(tc.path, tc.reserved); err != nil {
				t.Errorf("expected valid, got %v", err)
			}
		})
	}
}

func TestValidatePathWithReserved_Rejected(t *testing.T) {
	cases := []struct {
		path     string
		reserved string
	}{
		{"_scrinium", "_scrinium"},
		{"_scrinium/anything", "_scrinium"},
		{"_scrinium/deep/path", "_scrinium"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			err := fsmeta.ValidatePathWithReserved(tc.path, tc.reserved)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, errs.ErrInvalidPath) {
				t.Errorf("expected ErrInvalidPath, got %v", err)
			}
		})
	}
}

// --- Encode ---

func TestEncode_HappyPath(t *testing.T) {
	fs := fsmeta.FileSystem{
		Path:    "photos/2024/img.jpg",
		Mode:    0644,
		UID:     1000,
		GID:     1000,
		ModTime: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC),
		MIME:    "image/jpeg",
	}
	raw, err := fsmeta.Encode(fs)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Round-trip parse to a generic map to verify the shape and the
	// auto-filled Kind.
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if got := m["kind"]; got != fsmeta.Marker {
		t.Errorf("kind: got %q, want %q", got, fsmeta.Marker)
	}
	if got := m["path"]; got != "photos/2024/img.jpg" {
		t.Errorf("path: got %q, want %q", got, "photos/2024/img.jpg")
	}
	if got := m["mime"]; got != "image/jpeg" {
		t.Errorf("mime: got %q, want %q", got, "image/jpeg")
	}
}

func TestEncode_FillsKindWhenEmpty(t *testing.T) {
	// Caller may leave Kind unset; Encode injects Marker.
	fs := fsmeta.FileSystem{Path: "a"}
	raw, err := fsmeta.Encode(fs)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(string(raw), `"kind":"`+fsmeta.Marker+`"`) {
		t.Errorf("expected kind to be auto-filled; got %s", raw)
	}
}

func TestEncode_OverridesKindWhenSet(t *testing.T) {
	// If a caller passes a wrong Kind, Encode overwrites it. The
	// emitted bytes always carry the canonical Marker — the schema
	// is the contract, not the input.
	fs := fsmeta.FileSystem{Kind: "wrong/v999", Path: "a"}
	raw, err := fsmeta.Encode(fs)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.Contains(string(raw), `"kind":"`+fsmeta.Marker+`"`) {
		t.Errorf("expected kind override; got %s", raw)
	}
	if strings.Contains(string(raw), "wrong/v999") {
		t.Errorf("expected wrong kind to be replaced; got %s", raw)
	}
}

func TestEncode_RejectsInvalidPath(t *testing.T) {
	cases := []fsmeta.FileSystem{
		{Path: ""},
		{Path: "/leading"},
		{Path: "double//slash"},
		{Path: "with\x00nul"},
		{Path: "with/.."},
	}
	for i, fs := range cases {
		_, err := fsmeta.Encode(fs)
		if err == nil {
			t.Errorf("case %d (%q): expected error, got nil", i, fs.Path)
			continue
		}
		if !errors.Is(err, errs.ErrInvalidPath) {
			t.Errorf("case %d: expected ErrInvalidPath, got %v", i, err)
		}
	}
}

func TestEncode_OmitemptyOptionalFields(t *testing.T) {
	// Mode/UID/GID = 0 and empty MIME/zero ModTime should not
	// appear in the JSON. Keeps the wire format compact.
	fs := fsmeta.FileSystem{Path: "a"}
	raw, err := fsmeta.Encode(fs)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	s := string(raw)
	for _, omitted := range []string{"mode", "uid", "gid", "mime", "modTime"} {
		if strings.Contains(s, `"`+omitted+`"`) {
			t.Errorf("expected %q to be omitted; got %s", omitted, s)
		}
	}
}

// --- Decode ---

func TestDecode_RoundTrip(t *testing.T) {
	in := fsmeta.FileSystem{
		Path:    "photos/img.jpg",
		Mode:    0644,
		UID:     1000,
		GID:     1000,
		ModTime: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC),
		MIME:    "image/jpeg",
	}
	raw, err := fsmeta.Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, ok, err := fsmeta.Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if out.Path != in.Path {
		t.Errorf("Path: got %q, want %q", out.Path, in.Path)
	}
	if out.Mode != in.Mode {
		t.Errorf("Mode: got %d, want %d", out.Mode, in.Mode)
	}
	if out.UID != in.UID || out.GID != in.GID {
		t.Errorf("UID/GID: got %d/%d, want %d/%d", out.UID, out.GID, in.UID, in.GID)
	}
	if !out.ModTime.Equal(in.ModTime) {
		t.Errorf("ModTime: got %v, want %v", out.ModTime, in.ModTime)
	}
	if out.MIME != in.MIME {
		t.Errorf("MIME: got %q, want %q", out.MIME, in.MIME)
	}
	if out.Kind != fsmeta.Marker {
		t.Errorf("Kind: got %q, want %q", out.Kind, fsmeta.Marker)
	}
}

func TestDecode_EmptyMetadata(t *testing.T) {
	// Empty metadata is the common "no schema" case. Not an error.
	for _, raw := range []json.RawMessage{nil, {}, json.RawMessage("")} {
		fs, ok, err := fsmeta.Decode(raw)
		if err != nil {
			t.Errorf("expected no error for empty metadata, got %v", err)
		}
		if ok {
			t.Errorf("expected ok=false for empty metadata")
		}
		if fs.Path != "" {
			t.Errorf("expected zero FileSystem, got %+v", fs)
		}
	}
}

func TestDecode_ForeignSchema(t *testing.T) {
	// Metadata in a different schema must produce (zero, false,
	// nil) — not an error. Coexistence with other schemas is part
	// of the contract.
	cases := [][]byte{
		[]byte(`{"kind":"email/v1","subject":"hi","from":"a@b"}`),
		[]byte(`{"author":"alice","title":"x"}`),
		[]byte(`{}`),
		[]byte(`{"kind":"scrinium.fs/v2","path":"x"}`), // future version
	}
	for _, raw := range cases {
		fs, ok, err := fsmeta.Decode(raw)
		if err != nil {
			t.Errorf("input %s: expected no error, got %v", raw, err)
		}
		if ok {
			t.Errorf("input %s: expected ok=false", raw)
		}
		_ = fs
	}
}

func TestDecode_BrokenJSON(t *testing.T) {
	// Malformed JSON cannot be probed for kind. We treat it as
	// "not us" and return (zero, false, nil) — the caller lacks
	// the means to know whose data this is. The error path is
	// reserved for the case where we *know* the artifact intended
	// the schema (kind matches) but the rest is broken.
	cases := [][]byte{
		[]byte(`{not json`),
		[]byte(`{"kind":}`),
		[]byte(`null garbage`),
	}
	for _, raw := range cases {
		fs, ok, err := fsmeta.Decode(raw)
		if err != nil {
			t.Errorf("input %s: expected no error (foreign-treatment), got %v", raw, err)
		}
		if ok {
			t.Errorf("input %s: expected ok=false", raw)
		}
		_ = fs
	}
}

func TestDecode_MarkerMatchInvalidPath(t *testing.T) {
	// Kind matches Marker but Path is broken — the artifact
	// intended this schema and got it wrong; surface as error.
	raw := []byte(`{"kind":"` + fsmeta.Marker + `","path":"/leading/slash"}`)
	_, ok, err := fsmeta.Decode(raw)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, errs.ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath, got %v", err)
	}
	if ok {
		t.Errorf("expected ok=false")
	}
}

func TestDecode_MarkerMatchEmptyPath(t *testing.T) {
	// Same idea: Path is required.
	raw := []byte(`{"kind":"` + fsmeta.Marker + `"}`)
	_, ok, err := fsmeta.Decode(raw)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, errs.ErrInvalidPath) {
		t.Errorf("expected ErrInvalidPath, got %v", err)
	}
	if ok {
		t.Errorf("expected ok=false")
	}
}

func TestDecode_MarkerMatchBrokenBody(t *testing.T) {
	// Kind matches but the rest of the JSON is broken (mode is a
	// string instead of a number). Decode returns (zero, false, err).
	raw := []byte(`{"kind":"` + fsmeta.Marker + `","path":"a","mode":"not-a-number"}`)
	_, ok, err := fsmeta.Decode(raw)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	// We do not require ErrInvalidPath here — this is a JSON shape
	// error, not a path error. Just verify the call surfaces it.
	if ok {
		t.Errorf("expected ok=false")
	}
}

// --- Resolver ---

func TestResolver_Found(t *testing.T) {
	raw, err := fsmeta.Encode(fsmeta.FileSystem{Path: "photos/img.jpg"})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	m := domain.Manifest{Metadata: raw}
	path, ok := fsmeta.Resolver(m)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if path != "photos/img.jpg" {
		t.Errorf("path: got %q, want %q", path, "photos/img.jpg")
	}
}

func TestResolver_EmptyMetadata(t *testing.T) {
	m := domain.Manifest{Metadata: nil}
	path, ok := fsmeta.Resolver(m)
	if ok {
		t.Errorf("expected ok=false, got path=%q", path)
	}
}

func TestResolver_ForeignSchema(t *testing.T) {
	m := domain.Manifest{Metadata: []byte(`{"kind":"email/v1","subject":"hi"}`)}
	path, ok := fsmeta.Resolver(m)
	if ok {
		t.Errorf("expected ok=false for foreign schema, got path=%q", path)
	}
}

func TestResolver_SwallowsDecodeErrors(t *testing.T) {
	// A malformed payload that *says* it is fsmeta — Resolver must
	// silently return ("", false). This is the hot-path
	// requirement: a single bad artifact must not break the View
	// backfill.
	raw := []byte(`{"kind":"` + fsmeta.Marker + `","path":"/leading"}`)
	m := domain.Manifest{Metadata: raw}
	path, ok := fsmeta.Resolver(m)
	if ok {
		t.Errorf("expected ok=false for invalid path, got path=%q", path)
	}
}
