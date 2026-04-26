package descriptor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/rkurbatov/scrinium/driver"
)

// CurrentFormatVersion is the version this build of the package
// writes. Incremented on any breaking change to the on-disk shape
// of store.json. Forward compatibility is one-way: a binary
// reading a descriptor with FormatVersion > CurrentFormatVersion
// must refuse to open the Store, just as for StoreIndex.
const CurrentFormatVersion = 1

// Path is the conventional location of the descriptor inside a
// Store's Location. Relative to the Driver root.
const Path = "store.json"

// Descriptor is the on-disk shape of store.json. The struct is the
// JSON schema: every exported field is a key, JSON tags fix the
// names so renaming the Go field never breaks the format.
//
// Fields are ordered top-down by stability:
//   - identity (never changes)
//   - format version (changes on schema migrations)
//   - immutable StoreConfig snapshot (set at InitStore, validated
//     at every OpenStore)
//   - audit trail (createdAt, lastWrittenAt)
//
// All immutable params are captured as plain strings/ints rather
// than typed enums. The descriptor file should be readable by any
// future binary, including ones that don't recognise newer enum
// values; storing strings preserves the option to upcast on read.
type Descriptor struct {
	// StoreID — the global identity of this Store. UUID v4,
	// generated once at InitStore, never changes.
	StoreID string `json:"storeId"`

	// FormatVersion of store.json itself.
	FormatVersion int `json:"formatVersion"`

	// Immutable StoreConfig snapshot. None of these can change
	// after InitStore; OpenStore validates them strictly.
	PathTopology     string `json:"pathTopology"`
	ManifestStorage  string `json:"manifestStorage"`
	ManifestEncoding string `json:"manifestEncoding"`
	ManifestCrypto   string `json:"manifestCrypto"`
	ContentHasher    string `json:"contentHasher"`

	// DeletionPolicyLock — the only flag from DeletionPolicy that
	// is itself immutable (it locks the policy from being
	// downgraded away from NoDelete via UpdateConfig). The policy
	// value itself is mutable and lives in the active StoreConfig
	// artifact, not here.
	DeletionPolicyLock bool `json:"deletionPolicyLock"`

	// CreatedAt and LastWrittenAt are RFC 3339 timestamps. We
	// store strings (not time.Time encoded as JSON) so a human
	// reading the file can immediately tell the date.
	CreatedAt     string `json:"createdAt"`
	LastWrittenAt string `json:"lastWrittenAt"`
}

// Validate performs syntactic and structural checks on the
// descriptor. It does NOT compare against an external StoreConfig;
// that comparison is the caller's job (core.OpenStore).
func (d *Descriptor) Validate() error {
	if d.StoreID == "" {
		return errors.New("descriptor: empty storeId")
	}
	if d.FormatVersion <= 0 {
		return fmt.Errorf("descriptor: invalid formatVersion: %d", d.FormatVersion)
	}
	if d.FormatVersion > CurrentFormatVersion {
		return fmt.Errorf("descriptor: formatVersion %d exceeds supported %d",
			d.FormatVersion, CurrentFormatVersion)
	}
	if d.PathTopology == "" {
		return errors.New("descriptor: empty pathTopology")
	}
	if d.ManifestStorage == "" {
		return errors.New("descriptor: empty manifestStorage")
	}
	if d.ManifestEncoding == "" {
		return errors.New("descriptor: empty manifestEncoding")
	}
	if d.ManifestCrypto == "" {
		return errors.New("descriptor: empty manifestCrypto")
	}
	if d.ContentHasher == "" {
		return errors.New("descriptor: empty contentHasher")
	}
	if _, err := time.Parse(time.RFC3339Nano, d.CreatedAt); err != nil {
		return fmt.Errorf("descriptor: invalid createdAt: %w", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, d.LastWrittenAt); err != nil {
		return fmt.Errorf("descriptor: invalid lastWrittenAt: %w", err)
	}
	return nil
}

// Marshal serialises d to pretty-printed JSON with a trailing
// newline. Pretty-printing is intentional: the file is a debugging
// surface, not a hot-path payload, and a few extra bytes are not
// worth fighting for. The trailing newline keeps the file
// well-formed for POSIX text utilities.
func Marshal(d *Descriptor) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Unmarshal parses bytes into a Descriptor and validates them.
// A successfully unmarshalled descriptor is guaranteed to satisfy
// Validate(); the caller does not need to repeat the check.
func Unmarshal(data []byte) (*Descriptor, error) {
	var d Descriptor
	dec := json.NewDecoder(bytesReader(data))
	dec.DisallowUnknownFields() // catch typos in hand-edited files
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("descriptor: parse: %w", err)
	}
	// json.Decoder leaves trailing whitespace untouched; verify
	// there is no second JSON document in the buffer (a defensive
	// check against accidentally appended content).
	if dec.More() {
		return nil, errors.New("descriptor: trailing content after JSON object")
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// bytesReader wraps a byte slice into an io.Reader. Used to feed
// json.Decoder in Unmarshal so we get DisallowUnknownFields without
// constructing a *bytes.Reader at every call site.
func bytesReader(b []byte) io.Reader {
	return &sliceReader{b: b}
}

type sliceReader struct {
	b   []byte
	off int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.off:])
	r.off += n
	return n, nil
}

// Read pulls the descriptor through a Driver. It performs a full
// read into memory because the descriptor is bounded (single
// kilobytes); streaming would only complicate the JSON parse.
//
// Returns os.ErrNotExist (via the driver) when no descriptor is
// present at the standard Path; the caller distinguishes a fresh
// Location from a corrupted one based on this signal.
func Read(ctx context.Context, drv driver.Driver) (*Descriptor, error) {
	rc, err := drv.Get(ctx, Path)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("descriptor: read: %w", err)
	}
	return Unmarshal(data)
}

// Write serialises and stores the descriptor through a Driver.
// LastWrittenAt is set to time.Now() on every Write — the field is
// mutated even when the rest of the struct is unchanged, so the
// file's mtime always reflects the latest engine touch.
//
// Driver.Put is atomic (temp + rename); a parallel Read from
// another process never observes a partial descriptor.
func Write(ctx context.Context, drv driver.Driver, d *Descriptor) error {
	d.LastWrittenAt = time.Now().UTC().Format(time.RFC3339Nano)
	if d.CreatedAt == "" {
		d.CreatedAt = d.LastWrittenAt
	}
	data, err := Marshal(d)
	if err != nil {
		return err
	}
	return drv.Put(ctx, Path, bytesReader(data))
}
