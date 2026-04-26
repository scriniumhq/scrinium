package descriptor

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/driver/localfs"
)

// helper: build a minimal valid descriptor for tests.
func validDescriptor() *Descriptor {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return &Descriptor{
		StoreID:          "11111111-2222-3333-4444-555555555555",
		FormatVersion:    CurrentFormatVersion,
		PathTopology:     "Sharded",
		ManifestStorage:  "Remote",
		ManifestEncoding: "JSON",
		ManifestCrypto:   "Plain",
		ContentHasher:    "sha256",
		CreatedAt:        now,
		LastWrittenAt:    now,
	}
}

// --- Validate ---

func TestValidate_OK(t *testing.T) {
	if err := validDescriptor().Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidate_RejectsEmptyStoreID(t *testing.T) {
	d := validDescriptor()
	d.StoreID = ""
	if err := d.Validate(); err == nil {
		t.Fatal("expected error on empty StoreID")
	}
}

func TestValidate_RejectsZeroFormatVersion(t *testing.T) {
	d := validDescriptor()
	d.FormatVersion = 0
	if err := d.Validate(); err == nil {
		t.Fatal("expected error on zero FormatVersion")
	}
}

func TestValidate_RejectsFutureFormatVersion(t *testing.T) {
	d := validDescriptor()
	d.FormatVersion = CurrentFormatVersion + 1
	err := d.Validate()
	if err == nil {
		t.Fatal("expected error on future FormatVersion")
	}
	if !strings.Contains(err.Error(), "exceeds supported") {
		t.Errorf("error should mention version mismatch: %v", err)
	}
}

func TestValidate_RejectsBadTimestamp(t *testing.T) {
	d := validDescriptor()
	d.CreatedAt = "not-a-timestamp"
	if err := d.Validate(); err == nil {
		t.Fatal("expected error on bad CreatedAt")
	}
}

func TestValidate_RejectsEmptyImmutableField(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Descriptor)
	}{
		{"PathTopology", func(d *Descriptor) { d.PathTopology = "" }},
		{"ManifestStorage", func(d *Descriptor) { d.ManifestStorage = "" }},
		{"ManifestEncoding", func(d *Descriptor) { d.ManifestEncoding = "" }},
		{"ManifestCrypto", func(d *Descriptor) { d.ManifestCrypto = "" }},
		{"ContentHasher", func(d *Descriptor) { d.ContentHasher = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := validDescriptor()
			c.mut(d)
			if err := d.Validate(); err == nil {
				t.Errorf("expected error on empty %s", c.name)
			}
		})
	}
}

// --- Marshal / Unmarshal ---

func TestMarshalUnmarshal_RoundTrip(t *testing.T) {
	src := validDescriptor()
	data, err := Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if data[len(data)-1] != '\n' {
		t.Error("Marshal output should end with newline")
	}

	got, err := Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got.StoreID != src.StoreID {
		t.Errorf("StoreID: got %q, want %q", got.StoreID, src.StoreID)
	}
	if got.FormatVersion != src.FormatVersion {
		t.Errorf("FormatVersion: got %d, want %d", got.FormatVersion, src.FormatVersion)
	}
	if got.ManifestCrypto != src.ManifestCrypto {
		t.Errorf("ManifestCrypto: got %q, want %q", got.ManifestCrypto, src.ManifestCrypto)
	}
}

func TestMarshal_PrettyPrinted(t *testing.T) {
	data, err := Marshal(validDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	// Pretty-printed JSON has a newline between top-level fields;
	// a one-liner would be a single byte sequence with no '\n'
	// inside the braces.
	innerNewlines := 0
	for _, b := range data {
		if b == '\n' {
			innerNewlines++
		}
	}
	if innerNewlines < 5 {
		t.Errorf("expected pretty-printed JSON with multiple lines, got %d newlines", innerNewlines)
	}
}

func TestUnmarshal_RejectsUnknownField(t *testing.T) {
	bad := []byte(`{
		"storeId": "11111111-2222-3333-4444-555555555555",
		"formatVersion": 1,
		"pathTopology": "Sharded",
		"manifestStorage": "Remote",
		"manifestEncoding": "JSON",
		"manifestCrypto": "Plain",
		"contentHasher": "sha256",
		"deletionPolicyLock": false,
		"createdAt": "2025-01-01T00:00:00Z",
		"lastWrittenAt": "2025-01-01T00:00:00Z",
		"unknownExtraField": "value"
	}`)
	_, err := Unmarshal(bad)
	if err == nil {
		t.Fatal("expected error on unknown field")
	}
}

func TestUnmarshal_RejectsTrailingContent(t *testing.T) {
	d, _ := Marshal(validDescriptor())
	bad := append(d, []byte(`{"another":"document"}`)...)
	_, err := Unmarshal(bad)
	if err == nil {
		t.Fatal("expected error on trailing content")
	}
	if !strings.Contains(err.Error(), "trailing") {
		t.Errorf("error should mention trailing content: %v", err)
	}
}

func TestUnmarshal_RejectsMalformedJSON(t *testing.T) {
	_, err := Unmarshal([]byte(`{not json`))
	if err == nil {
		t.Fatal("expected parse error")
	}
}

// --- Read / Write through localfs ---

func newDriver(t *testing.T) *localfs.Driver {
	t.Helper()
	drv, err := localfs.New(t.TempDir(), localfs.WithFsync(false))
	if err != nil {
		t.Fatal(err)
	}
	return drv
}

func TestWrite_Read_RoundTrip(t *testing.T) {
	drv := newDriver(t)
	src := validDescriptor()

	if err := Write(context.Background(), drv, src); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Read(context.Background(), drv)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.StoreID != src.StoreID {
		t.Errorf("StoreID round-trip: got %q, want %q", got.StoreID, src.StoreID)
	}
	if got.LastWrittenAt == "" {
		t.Error("LastWrittenAt should be populated by Write")
	}
}

func TestWrite_PopulatesCreatedAtOnFirstWrite(t *testing.T) {
	drv := newDriver(t)
	d := validDescriptor()
	d.CreatedAt = "" // fresh descriptor — let Write populate it.
	d.LastWrittenAt = ""

	if err := Write(context.Background(), drv, d); err != nil {
		t.Fatal(err)
	}
	if d.CreatedAt == "" {
		t.Error("CreatedAt was not populated")
	}
	if d.LastWrittenAt == "" {
		t.Error("LastWrittenAt was not populated")
	}
	if d.CreatedAt != d.LastWrittenAt {
		t.Errorf("on first write CreatedAt and LastWrittenAt should match: %q vs %q",
			d.CreatedAt, d.LastWrittenAt)
	}
}

func TestWrite_AdvancesLastWrittenAt(t *testing.T) {
	drv := newDriver(t)
	d := validDescriptor()
	if err := Write(context.Background(), drv, d); err != nil {
		t.Fatal(err)
	}
	first := d.LastWrittenAt

	// Sleep just enough so the new RFC3339Nano differs.
	time.Sleep(2 * time.Millisecond)

	if err := Write(context.Background(), drv, d); err != nil {
		t.Fatal(err)
	}
	if d.LastWrittenAt == first {
		t.Errorf("LastWrittenAt did not advance: %q", first)
	}
	if d.CreatedAt == d.LastWrittenAt {
		t.Errorf("CreatedAt should not advance after first write")
	}
}

func TestRead_NotFound(t *testing.T) {
	drv := newDriver(t)
	_, err := Read(context.Background(), drv)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
}
