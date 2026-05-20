package fsmeta

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/errs"
)

// Marker identifies the schema and version. Decode rejects any
// payload whose Kind does not match.
const Marker = "scrinium.fs/v1"

// FileSystem is the parsed payload of Manifest.Metadata for the
// filesystem schema. Only Path is mandatory; the rest fall back
// to FSOps defaults (or Manifest.CreatedAt for ModTime) when zero.
type FileSystem struct {
	// Kind must equal Marker for the artifact to be recognised by
	// this schema's decoder.
	Kind string `json:"kind"`

	// Path is the virtual path of the artifact, slash-separated,
	// no leading slash, no "..", no NUL bytes, no empty segments.
	// Example: "photos/2024/01/sunrise.jpg".
	Path string `json:"path"`

	// Mode are POSIX mode bits. Zero means "use default".
	Mode uint32 `json:"mode,omitempty"`

	// UID is the POSIX user id. Zero means "use default".
	UID uint32 `json:"uid,omitempty"`

	// GID is the POSIX group id. Zero means "use default".
	GID uint32 `json:"gid,omitempty"`

	// ModTime is the POSIX mtime. Zero means "use
	// Manifest.CreatedAt". Custom MarshalJSON omits the field when
	// zero — encoding/json's `omitempty` does not recognise zero
	// time.Time, so we handle it explicitly.
	ModTime time.Time `json:"-"`

	// MIME is the artifact's MIME type. Empty means
	// "application/octet-stream" or transport-side detection.
	MIME string `json:"mime,omitempty"`
}

// MarshalJSON serialises FileSystem with proper omitempty for the
// fields that encoding/json's tag does not handle:
//   - ModTime: time.Time zero value is not "empty" by the standard
//     marshaller, so we drop the key when IsZero.
//
// All other fields use the standard tagged behaviour.
func (fs FileSystem) MarshalJSON() ([]byte, error) {
	// Mirror struct, but with ModTime as a pointer so the standard
	// `omitempty` machinery works on it. We fill the pointer only
	// when the source ModTime is non-zero.
	type wireFormat struct {
		Kind    string     `json:"kind"`
		Path    string     `json:"path"`
		Mode    uint32     `json:"mode,omitempty"`
		UID     uint32     `json:"uid,omitempty"`
		GID     uint32     `json:"gid,omitempty"`
		ModTime *time.Time `json:"modTime,omitempty"`
		MIME    string     `json:"mime,omitempty"`
	}
	w := wireFormat{
		Kind: fs.Kind,
		Path: fs.Path,
		Mode: fs.Mode,
		UID:  fs.UID,
		GID:  fs.GID,
		MIME: fs.MIME,
	}
	if !fs.ModTime.IsZero() {
		t := fs.ModTime
		w.ModTime = &t
	}
	return json.Marshal(w)
}

// UnmarshalJSON mirrors MarshalJSON: it accepts the wire form (with
// optional modTime) and produces a FileSystem with the zero time
// when the field is absent. The standard json.Unmarshal would
// already do this for plain time.Time, but we keep the explicit
// path through the wire-shape to stay symmetric with MarshalJSON
// and to lock in the on-wire field name regardless of any future
// refactors of the public struct.
func (fs *FileSystem) UnmarshalJSON(data []byte) error {
	type wireFormat struct {
		Kind    string     `json:"kind"`
		Path    string     `json:"path"`
		Mode    uint32     `json:"mode,omitempty"`
		UID     uint32     `json:"uid,omitempty"`
		GID     uint32     `json:"gid,omitempty"`
		ModTime *time.Time `json:"modTime,omitempty"`
		MIME    string     `json:"mime,omitempty"`
	}
	var w wireFormat
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	fs.Kind = w.Kind
	fs.Path = w.Path
	fs.Mode = w.Mode
	fs.UID = w.UID
	fs.GID = w.GID
	fs.MIME = w.MIME
	if w.ModTime != nil {
		fs.ModTime = *w.ModTime
	} else {
		fs.ModTime = time.Time{}
	}
	return nil
}

// Encode validates a FileSystem and serialises it for use as
// Manifest.Metadata. Kind is filled in automatically: callers may
// leave it empty.
//
// Returns an error wrapping errs.ErrInvalidPath when Path fails
// validation.
func Encode(fs FileSystem) (json.RawMessage, error) {
	if err := ValidatePath(fs.Path); err != nil {
		return nil, err
	}
	fs.Kind = Marker
	out, err := json.Marshal(fs)
	if err != nil {
		// Marshalling a struct of basic types should not fail; if
		// it does, the failure is a programming error in the
		// schema definition rather than runtime data.
		return nil, fmt.Errorf("fsmeta.Encode: %w", err)
	}
	return out, nil
}

// Decode interprets raw Manifest.Metadata. The triple return
// (FileSystem, ok, error) lets callers distinguish three
// outcomes:
//
//   - (zero, false, nil) — raw is empty, or the payload is valid
//     JSON but its Kind does not match Marker, or the payload is
//     not valid JSON at all. Common case for artifacts written by
//     other schemas; not an error.
//   - (fs, true, nil) — Kind matches and Path is valid. fs is
//     usable.
//   - (zero, false, err) — Kind matches but the payload is broken
//     (malformed body, invalid Path, etc.). The artifact intended
//     to use this schema and got it wrong; surface the error.
//
// Decode never panics on adversarial input.
func Decode(raw json.RawMessage) (FileSystem, bool, error) {
	if len(raw) == 0 {
		return FileSystem{}, false, nil
	}
	// Probe the kind first using a minimal struct. This is cheap
	// and lets us reject foreign-schema payloads without burning
	// time on the full unmarshal.
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		// Truly broken JSON. We cannot tell whether the host
		// intended this schema or not; treat as "not us" and let
		// the caller decide. The error path is reserved for the
		// case where Kind matches but the rest is broken.
		return FileSystem{}, false, nil
	}
	if probe.Kind != Marker {
		return FileSystem{}, false, nil
	}
	var fs FileSystem
	if err := json.Unmarshal(raw, &fs); err != nil {
		return FileSystem{}, false, fmt.Errorf("fsmeta.Decode: %w", err)
	}
	if err := ValidatePath(fs.Path); err != nil {
		return FileSystem{}, false, err
	}
	return fs, true, nil
}

// Resolver is the projection.PathResolver for the filesystem
// schema. It returns (Path, true) for artifacts whose metadata
// decodes cleanly; otherwise ("", false).
//
// Note that a malformed payload also returns ("", false) — the
// artifact ends up in the orphaned tree. The decode error is
// silently swallowed because the resolver runs inside the View's
// hot path and a single bad artifact must not fail backfill.
// Surfacing such errors is the ingester's job at write time.
func Resolver(m domain.Manifest) (string, bool) {
	fs, ok, err := Decode(domain.EffectiveExt(m))
	if err != nil || !ok {
		return "", false
	}
	return fs.Path, true
}

// --- Path validation ---

// ValidatePath checks the rules for a virtual path:
//
//   - non-empty
//   - no leading slash (the path is relative)
//   - no NUL bytes
//   - no empty segments (which would arise from "//" or a trailing
//     "/")
//   - no "." or ".." segments
//
// Returns an error wrapping errs.ErrInvalidPath when any rule is
// broken. All checks performed; the error message names the first
// failed rule.
//
// Reserved-root checking is a separate concern because the
// reserved name (FUSE service prefix) is configurable by the
// transport. Use ValidatePathWithReserved to combine.
func ValidatePath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty path", errs.ErrInvalidPath)
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("%w: path contains NUL", errs.ErrInvalidPath)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: path starts with /", errs.ErrInvalidPath)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return fmt.Errorf("%w: empty segment in %q", errs.ErrInvalidPath, p)
		case ".":
			return fmt.Errorf("%w: %q contains . segment", errs.ErrInvalidPath, p)
		case "..":
			return fmt.Errorf("%w: %q contains .. segment", errs.ErrInvalidPath, p)
		}
	}
	return nil
}

// ValidatePathWithReserved performs ValidatePath plus a check that
// the first path segment does not equal the reserved name. The
// reserved name is the FUSE service prefix (default "_scrinium"),
// passed by the transport.
//
// An empty reserved string disables the reserved-root check —
// equivalent to ValidatePath.
func ValidatePathWithReserved(p, reserved string) error {
	if err := ValidatePath(p); err != nil {
		return err
	}
	if reserved == "" {
		return nil
	}
	first := p
	if i := strings.IndexByte(p, '/'); i >= 0 {
		first = p[:i]
	}
	if first == reserved {
		return fmt.Errorf("%w: first segment %q is reserved", errs.ErrInvalidPath, reserved)
	}
	return nil
}
