package vfsmeta_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/errs"
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
			if err := vfsmeta.ValidatePath(p); err != nil {
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
			err := vfsmeta.ValidatePath(tc.path)
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
			if err := vfsmeta.ValidatePathWithReserved(tc.path, tc.reserved); err != nil {
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
			err := vfsmeta.ValidatePathWithReserved(tc.path, tc.reserved)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, errs.ErrInvalidPath) {
				t.Errorf("expected ErrInvalidPath, got %v", err)
			}
		})
	}
}

// --- Embed ---

// extValue pulls the raw payload stored under the "vfsmeta" key of an
// Ext object, for shape assertions.
func extValue(t *testing.T, ext json.RawMessage) map[string]any {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(ext, &obj); err != nil {
		t.Fatalf("Ext is not a JSON object: %v", err)
	}
	raw, ok := obj["vfsmeta"]
	if !ok {
		t.Fatalf("Ext has no \"vfsmeta\" key: %s", ext)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("vfsmeta value is not a JSON object: %v", err)
	}
	return v
}

func TestEmbed_HappyPath(t *testing.T) {
	fs := vfsmeta.FileSystem{
		Path:    "photos/2024/img.jpg",
		Mode:    0644,
		UID:     1000,
		GID:     1000,
		ModTime: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC),
		MIME:    "image/jpeg",
	}
	ext, err := vfsmeta.Embed(nil, fs)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	v := extValue(t, ext)
	if got := v["version"]; got != float64(vfsmeta.Version) {
		t.Errorf("version: got %v, want %d", got, vfsmeta.Version)
	}
	if got := v["path"]; got != "photos/2024/img.jpg" {
		t.Errorf("path: got %q, want %q", got, "photos/2024/img.jpg")
	}
	if got := v["mime"]; got != "image/jpeg" {
		t.Errorf("mime: got %q, want %q", got, "image/jpeg")
	}
	if _, ok := v["kind"]; ok {
		t.Errorf("payload must not carry a \"kind\" field; got %s", ext)
	}
}

func TestEmbed_PreservesOtherSchemas(t *testing.T) {
	// Embedding into an Ext that already carries another schema's key
	// (here "nsid") must keep that key intact.
	existing := json.RawMessage(`{"nsid":"ns-uuid-7"}`)
	ext, err := vfsmeta.Embed(existing, vfsmeta.FileSystem{Path: "a"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(ext, &obj); err != nil {
		t.Fatalf("Ext: %v", err)
	}
	if _, ok := obj["vfsmeta"]; !ok {
		t.Errorf("vfsmeta key missing after Embed: %s", ext)
	}
	if got := string(obj["nsid"]); got != `"ns-uuid-7"` {
		t.Errorf("nsid not preserved: got %s", got)
	}
}

func TestEmbed_FillsVersion(t *testing.T) {
	ext, err := vfsmeta.Embed(nil, vfsmeta.FileSystem{Path: "a"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if !strings.Contains(string(ext), `"version":1`) {
		t.Errorf("expected version to be filled; got %s", ext)
	}
}

func TestEmbed_RejectsInvalidPath(t *testing.T) {
	cases := []vfsmeta.FileSystem{
		{Path: ""},
		{Path: "/leading"},
		{Path: "double//slash"},
		{Path: "with\x00nul"},
		{Path: "with/.."},
	}
	for i, fs := range cases {
		_, err := vfsmeta.Embed(nil, fs)
		if err == nil {
			t.Errorf("case %d (%q): expected error, got nil", i, fs.Path)
			continue
		}
		if !errors.Is(err, errs.ErrInvalidPath) {
			t.Errorf("case %d: expected ErrInvalidPath, got %v", i, err)
		}
	}
}

func TestEmbed_RejectsNonObjectExt(t *testing.T) {
	_, err := vfsmeta.Embed(json.RawMessage(`["not","an","object"]`), vfsmeta.FileSystem{Path: "a"})
	if err == nil {
		t.Fatalf("expected error for non-object Ext, got nil")
	}
}

func TestEmbed_OmitemptyOptionalFields(t *testing.T) {
	// Mode/UID/GID = 0 and empty MIME/zero ModTime should not appear in
	// the payload. Keeps the wire format compact. version and path stay.
	ext, err := vfsmeta.Embed(nil, vfsmeta.FileSystem{Path: "a"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	s := string(ext)
	for _, omitted := range []string{"mode", "uid", "gid", "mime", "mtime"} {
		if strings.Contains(s, `"`+omitted+`"`) {
			t.Errorf("expected %q to be omitted; got %s", omitted, s)
		}
	}
}

// --- Decode ---

func TestDecode_RoundTrip(t *testing.T) {
	in := vfsmeta.FileSystem{
		Path:    "photos/img.jpg",
		Mode:    0644,
		UID:     1000,
		GID:     1000,
		ModTime: time.Date(2024, 5, 1, 10, 0, 0, 0, time.UTC),
		MIME:    "image/jpeg",
	}
	ext, err := vfsmeta.Embed(nil, in)
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	out, ok, err := vfsmeta.Decode(ext)
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
}

func TestDecode_EmptyExt(t *testing.T) {
	// Empty ext is the common "no schema" case. Not an error.
	for _, raw := range []json.RawMessage{nil, {}, json.RawMessage("")} {
		fs, ok, err := vfsmeta.Decode(raw)
		if err != nil {
			t.Errorf("expected no error for empty ext, got %v", err)
		}
		if ok {
			t.Errorf("expected ok=false for empty ext")
		}
		if fs.Path != "" {
			t.Errorf("expected zero FileSystem, got %+v", fs)
		}
	}
}

func TestDecode_NoVfsmetaKey(t *testing.T) {
	// An Ext object carrying only other schemas' keys must produce
	// (zero, false, nil) — coexistence is part of the contract.
	cases := [][]byte{
		[]byte(`{"namespace":{"nsid":"ns-1"}}`),
		[]byte(`{"nsid":"ns-1"}`),
		[]byte(`{"email":{"subject":"hi"}}`),
		[]byte(`{}`),
	}
	for _, raw := range cases {
		fs, ok, err := vfsmeta.Decode(raw)
		if err != nil {
			t.Errorf("input %s: expected no error, got %v", raw, err)
		}
		if ok {
			t.Errorf("input %s: expected ok=false", raw)
		}
		_ = fs
	}
}

func TestDecode_FutureVersion(t *testing.T) {
	// A vfsmeta payload of a different schema version is "not this v1
	// decoder's" — (zero, false, nil), so a future v2 decoder can claim
	// it rather than erroring here.
	raw := []byte(`{"vfsmeta":{"version":2,"path":"x"}}`)
	_, ok, err := vfsmeta.Decode(raw)
	if err != nil {
		t.Errorf("expected no error for future version, got %v", err)
	}
	if ok {
		t.Errorf("expected ok=false for future version")
	}
}

func TestDecode_BrokenJSON(t *testing.T) {
	// Malformed Ext cannot be parsed into a map. We treat it as "not us"
	// and return (zero, false, nil).
	cases := [][]byte{
		[]byte(`{not json`),
		[]byte(`{"vfsmeta":}`),
		[]byte(`null garbage`),
	}
	for _, raw := range cases {
		fs, ok, err := vfsmeta.Decode(raw)
		if err != nil {
			t.Errorf("input %s: expected no error (foreign-treatment), got %v", raw, err)
		}
		if ok {
			t.Errorf("input %s: expected ok=false", raw)
		}
		_ = fs
	}
}

func TestDecode_PresentInvalidPath(t *testing.T) {
	// The "vfsmeta" key is present and current version but Path is
	// broken — the artifact intended this schema and got it wrong;
	// surface as error.
	raw := []byte(`{"vfsmeta":{"version":1,"path":"/leading/slash"}}`)
	_, ok, err := vfsmeta.Decode(raw)
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

func TestDecode_PresentEmptyPath(t *testing.T) {
	// Same idea: Path is required.
	raw := []byte(`{"vfsmeta":{"version":1}}`)
	_, ok, err := vfsmeta.Decode(raw)
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

func TestDecode_PresentBrokenBody(t *testing.T) {
	// Key present, current version, but the payload is broken (mode is a
	// string instead of a number). Decode returns (zero, false, err).
	raw := []byte(`{"vfsmeta":{"version":1,"path":"a","mode":"not-a-number"}}`)
	_, ok, err := vfsmeta.Decode(raw)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if ok {
		t.Errorf("expected ok=false")
	}
}

// --- Resolver ---

func TestResolver_Found(t *testing.T) {
	ext, err := vfsmeta.Embed(nil, vfsmeta.FileSystem{Path: "photos/img.jpg"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	m := domain.Manifest{Ext: ext}
	path, ok := vfsmeta.Resolver(m)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if path != "photos/img.jpg" {
		t.Errorf("path: got %q, want %q", path, "photos/img.jpg")
	}
}

func TestResolver_EmptyMetadata(t *testing.T) {
	m := domain.Manifest{Ext: nil}
	path, ok := vfsmeta.Resolver(m)
	if ok {
		t.Errorf("expected ok=false, got path=%q", path)
	}
}

func TestResolver_NoVfsmetaKey(t *testing.T) {
	m := domain.Manifest{Ext: []byte(`{"email":{"subject":"hi"}}`)}
	path, ok := vfsmeta.Resolver(m)
	if ok {
		t.Errorf("expected ok=false for foreign schema, got path=%q", path)
	}
}

func TestResolver_SwallowsDecodeErrors(t *testing.T) {
	// A malformed payload under the vfsmeta key — Resolver must silently
	// return ("", false). This is the hot-path requirement: a single bad
	// artifact must not break the View backfill.
	raw := []byte(`{"vfsmeta":{"version":1,"path":"/leading"}}`)
	m := domain.Manifest{Ext: raw}
	path, ok := vfsmeta.Resolver(m)
	if ok {
		t.Errorf("expected ok=false for invalid path, got path=%q", path)
	}
}
