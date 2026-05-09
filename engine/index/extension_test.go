package index

import (
	"context"
	"errors"
	"testing"

	"scrinium.dev/engine/domain"
)

// TestEventKind_String covers the human-readable rendering. The
// string form lands in error messages and diagnostic events;
// regressing it silently breaks dashboards.
func TestEventKind_String(t *testing.T) {
	cases := []struct {
		kind EventKind
		want string
	}{
		{EventKindManifestIndexed, "ManifestIndexed"},
		{EventKindManifestDeleted, "ManifestDeleted"},
		{EventKindBlobRebound, "BlobRebound"},
		{EventKind(99), "EventKind(?)"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("EventKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// TestSentinels_Distinct asserts every sentinel error is distinct
// from every other. errors.Is must not produce false positives —
// callers branch on these.
func TestSentinels_Distinct(t *testing.T) {
	sentinels := []error{
		ErrStopScan,
		ErrExtensionExists,
		ErrSchemaRegression,
		ErrBackendMismatch,
		ErrEmptyPrefix,
	}
	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("sentinel[%d] errors.Is sentinel[%d] — should be distinct (%v == %v)",
					i, j, a, b)
			}
		}
	}
}

// TestSentinels_NotNil — defensive check that nobody silently
// stripped the var declarations.
func TestSentinels_NotNil(t *testing.T) {
	if ErrStopScan == nil ||
		ErrExtensionExists == nil ||
		ErrSchemaRegression == nil ||
		ErrBackendMismatch == nil ||
		ErrEmptyPrefix == nil {
		t.Fatal("a sentinel is nil")
	}
}

// --- compile-time conformance ---
//
// The following declarations only need to compile to assert that
// the contract types pin down the expected method-set shapes.
// They do not run at test time but participate in `go vet` and
// in the package-level type check.

type stubExtension struct{}

func (stubExtension) Name() string           { return "stub" }
func (stubExtension) SchemaVersion() int     { return 1 }
func (stubExtension) Subscribe() []EventKind { return nil }
func (stubExtension) Setup(ctx context.Context, store ExtensionStore, oldVersion int) error {
	return nil
}
func (stubExtension) Apply(ctx context.Context, store ExtensionStore, kind EventKind, args EventArgs) error {
	return nil
}
func (stubExtension) Close() error { return nil }

type stubStore struct{}

func (stubStore) Put(table, key string, value []byte) error { return nil }
func (stubStore) Get(table, key string) ([]byte, bool, error) {
	return nil, false, nil
}
func (stubStore) Delete(table, key string) error          { return nil }
func (stubStore) DeletePrefix(table, prefix string) error { return nil }
func (stubStore) Scan(table, prefix string, cb func(key string, value []byte) error) error {
	return nil
}
func (stubStore) Inc(table, key string, delta int64) (int64, error) { return 0, nil }

type stubRegistry struct{}

func (stubRegistry) Register(ctx context.Context, ext IndexExtension) error { return nil }

var (
	_ IndexExtension    = stubExtension{}
	_ ExtensionStore    = stubStore{}
	_ ExtensionRegistry = stubRegistry{}
)

// TestEventArgs_ZeroValueIsValid — the zero EventArgs is the
// shape backends pass for kinds that don't populate certain
// fields. Make sure it constructs cleanly without panic.
func TestEventArgs_ZeroValueIsValid(t *testing.T) {
	var args EventArgs
	if args.Manifest.ArtifactID != "" {
		t.Errorf("zero Manifest must have empty ArtifactID, got %q", args.Manifest.ArtifactID)
	}
	if args.ArtifactID != "" {
		t.Errorf("zero EventArgs.ArtifactID must be empty, got %q", args.ArtifactID)
	}
	if args.BlobRefs != nil {
		t.Errorf("zero BlobRefs must be nil, got %v", args.BlobRefs)
	}
}

// TestEventArgs_PopulatedShape demonstrates the per-kind shape
// described in the contract. This test is the executable form
// of the table in 3. Contracts/06 §6.4.
func TestEventArgs_PopulatedShape(t *testing.T) {
	m := domain.Manifest{ArtifactID: "art-1", Namespace: "files"}

	// ManifestIndexed: Manifest fully populated, ArtifactID dup.
	indexedArgs := EventArgs{Manifest: m, ArtifactID: m.ArtifactID}
	if indexedArgs.Manifest.ArtifactID != indexedArgs.ArtifactID {
		t.Error("ManifestIndexed: ArtifactID must duplicate Manifest.ArtifactID")
	}

	// ManifestDeleted: ArtifactID set, BlobRefs lists decremented blobs.
	deletedArgs := EventArgs{
		ArtifactID: "art-1",
		BlobRefs:   []string{"sha256:aaa", "sha256:bbb"},
	}
	if deletedArgs.Manifest.ArtifactID != "" {
		t.Error("ManifestDeleted: Manifest must be zero")
	}
	if len(deletedArgs.BlobRefs) != 2 {
		t.Errorf("ManifestDeleted: expected 2 blob refs, got %d", len(deletedArgs.BlobRefs))
	}

	// BlobRebound: only BlobRefs[0] set.
	reboundArgs := EventArgs{BlobRefs: []string{"sha256:xxx"}}
	if reboundArgs.Manifest.ArtifactID != "" || reboundArgs.ArtifactID != "" {
		t.Error("BlobRebound: Manifest and ArtifactID must be zero")
	}
}
