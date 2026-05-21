package storeconfig

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
	"testing"

	"crypto/sha256"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/driver/localfs"
	"scrinium.dev/engine/errs"
)

// These are white-box unit tests of the system.config pointer/format
// failure modes — they need no ArtifactWriter, only a seeded driver.
// The round-trip test (which exercises a real inline-artifact write)
// lives in engine/core, where the production ArtifactWriter is
// available; that is an integration test of the core↔storeconfig
// seam and belongs on the core side.

// testHashes is a minimal sha256-only domain.HashRegistry. Defined
// locally rather than reusing storefx.Hashes() because storefx
// imports engine/core, and core imports this package — pulling
// storefx in would create an import cycle in the test binary.
type testHashes struct{}

func (testHashes) Parse(h string) (string, []byte, error) {
	i := strings.IndexByte(h, '-')
	if i <= 0 {
		return "", nil, fmt.Errorf("malformed hash id %q", h)
	}
	raw, err := hex.DecodeString(h[i+1:])
	if err != nil {
		return "", nil, err
	}
	return h[:i], raw, nil
}

func (testHashes) NewHasher(algo string) (hash.Hash, error) {
	if algo == "sha256" {
		return sha256.New(), nil
	}
	return nil, fmt.Errorf("unknown algo %q", algo)
}

func (testHashes) Format(algo string, raw []byte) string {
	return algo + "-" + hex.EncodeToString(raw)
}

func (h testHashes) Register(string, func() hash.Hash) domain.HashRegistry { return h }

func newDriver(t *testing.T) *localfs.Driver {
	t.Helper()
	drv, err := localfs.New(t.TempDir(), localfs.WithFsync(false))
	if err != nil {
		t.Fatalf("localfs.New: %v", err)
	}
	return drv
}

func TestRead_Missing(t *testing.T) {
	drv := newDriver(t)
	_, err := Read(context.Background(), drv, testHashes{})
	if !errors.Is(err, errs.ErrMissingConfigPointer) {
		t.Fatalf("expected ErrMissingConfigPointer, got %v", err)
	}
}

func TestRead_CorruptedPointer(t *testing.T) {
	cases := []struct {
		name    string
		content []byte
	}{
		{"empty", []byte("")},
		{"whitespace only", []byte("   \n")},
		{"garbage", []byte("not-an-artifact-id\n")},
		{"missing dash", []byte("sha256deadbeef\n")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			drv := newDriver(t)
			if err := drv.Put(context.Background(), pointerPath,
				bytes.NewReader(c.content)); err != nil {
				t.Fatalf("seed pointer: %v", err)
			}
			_, err := Read(context.Background(), drv, testHashes{})
			if !errors.Is(err, errs.ErrCorruptedConfigPointer) {
				t.Fatalf("expected ErrCorruptedConfigPointer, got %v", err)
			}
		})
	}
}

func TestRead_DanglingPointer(t *testing.T) {
	drv := newDriver(t)
	// Syntactically valid ArtifactID, no manifest behind it.
	pointer := []byte("sha256-" +
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef\n")
	if err := drv.Put(context.Background(), pointerPath,
		bytes.NewReader(pointer)); err != nil {
		t.Fatalf("seed pointer: %v", err)
	}
	_, err := Read(context.Background(), drv, testHashes{})
	if !errors.Is(err, errs.ErrDanglingConfigPointer) {
		t.Fatalf("expected ErrDanglingConfigPointer, got %v", err)
	}
}

// ReadPointer is also exercised directly: a present, well-formed
// pointer parses to its ArtifactID.
func TestReadPointer_WellFormed(t *testing.T) {
	drv := newDriver(t)
	id := "sha256-" +
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if err := drv.Put(context.Background(), pointerPath,
		bytes.NewReader([]byte(id+"\n"))); err != nil {
		t.Fatalf("seed pointer: %v", err)
	}
	got, err := ReadPointer(context.Background(), drv, testHashes{})
	if err != nil {
		t.Fatalf("ReadPointer: %v", err)
	}
	if string(got) != id {
		t.Errorf("ReadPointer: got %q, want %q", got, id)
	}
}
