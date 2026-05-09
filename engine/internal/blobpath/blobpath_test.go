package blobpath_test

import (
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/internal/blobpath"
)

// --- Resolve: Sharded ---

func TestResolve_ShardedBlob(t *testing.T) {
	got, err := blobpath.Resolve(domain.PathTopologySharded, domain.BlobTypeRegular,
		"sha256-aabbccdd"+strings.Repeat("0", 56))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := "blobs/aa/bb/sha256-aabbccdd" + strings.Repeat("0", 56)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolve_ShardedChunk(t *testing.T) {
	got, err := blobpath.Resolve(domain.PathTopologySharded, domain.BlobTypeChunk,
		"sha256-deadbeef"+strings.Repeat("0", 56))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "chunks/de/ad/") {
		t.Errorf("expected chunks/de/ad/ prefix, got %q", got)
	}
}

func TestResolve_ShardedPack(t *testing.T) {
	got, err := blobpath.Resolve(domain.PathTopologySharded, domain.BlobTypePack,
		"sha256-12345678"+strings.Repeat("0", 56))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "packs/12/34/") {
		t.Errorf("expected packs/12/34/ prefix, got %q", got)
	}
}

// --- Resolve: Flat ---

func TestResolve_FlatBlob(t *testing.T) {
	ref := "sha256-aabbccdd" + strings.Repeat("0", 56)
	got, err := blobpath.Resolve(domain.PathTopologyFlat, domain.BlobTypeRegular, ref)
	if err != nil {
		t.Fatal(err)
	}
	want := "blobs/" + ref
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Case folding ---

func TestResolve_FoldsCaseInShards(t *testing.T) {
	// A ref with upper-case hex must shard into lower-case
	// directory names so the on-disk layout stays canonical.
	got, err := blobpath.Resolve(domain.PathTopologySharded, domain.BlobTypeRegular,
		"sha256-AABBCCDD"+strings.Repeat("0", 56))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "blobs/aa/bb/") {
		t.Errorf("expected lowercase shards, got %q", got)
	}
}

// --- Empty topology defaults to Sharded ---

func TestResolve_EmptyTopologyDefaultsToSharded(t *testing.T) {
	got, err := blobpath.Resolve(domain.PathTopology(""), domain.BlobTypeRegular,
		"sha256-aabbccdd"+strings.Repeat("0", 56))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "blobs/aa/bb/") {
		t.Errorf("empty topology should fall back to Sharded, got %q", got)
	}
}

// --- BlobType: Regular vs empty ---

func TestResolve_EmptyBlobTypeMeansRegular(t *testing.T) {
	got, err := blobpath.Resolve(domain.PathTopologySharded, domain.BlobType(""),
		"sha256-aabbccdd"+strings.Repeat("0", 56))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "blobs/") {
		t.Errorf("empty blob type should default to blobs/, got %q", got)
	}
}

// --- Resolve: error paths ---

func TestResolve_RejectsEmptyRef(t *testing.T) {
	_, err := blobpath.Resolve(domain.PathTopologySharded, domain.BlobTypeRegular, "")
	if err == nil {
		t.Fatal("expected error on empty ref")
	}
}

func TestResolve_RejectsRefWithoutAlgoPrefix(t *testing.T) {
	_, err := blobpath.Resolve(domain.PathTopologySharded, domain.BlobTypeRegular,
		"deadbeefcafe1234")
	if err == nil {
		t.Fatal("expected error on missing algo prefix")
	}
}

func TestResolve_RejectsTooShortHex(t *testing.T) {
	_, err := blobpath.Resolve(domain.PathTopologySharded, domain.BlobTypeRegular, "sha256-ab")
	if err == nil {
		t.Fatal("expected error on too-short hex")
	}
}

func TestResolve_RejectsNativeTopology(t *testing.T) {
	_, err := blobpath.Resolve(domain.PathTopologyNative, domain.BlobTypeRegular,
		"sha256-aabbccdd"+strings.Repeat("0", 56))
	if err == nil {
		t.Fatal("expected error on Native topology")
	}
}

func TestResolve_RejectsUnknownTopology(t *testing.T) {
	_, err := blobpath.Resolve(domain.PathTopology("Magic"), domain.BlobTypeRegular,
		"sha256-aabbccdd"+strings.Repeat("0", 56))
	if err == nil {
		t.Fatal("expected error on unknown topology")
	}
}

func TestResolve_RejectsUnknownBlobType(t *testing.T) {
	_, err := blobpath.Resolve(domain.PathTopologySharded, domain.BlobType("Mystery"),
		"sha256-aabbccdd"+strings.Repeat("0", 56))
	if err == nil {
		t.Fatal("expected error on unknown blob type")
	}
}

// --- ManifestPath ---

func TestManifestPath_Basic(t *testing.T) {
	got, err := blobpath.ManifestPath(domain.ArtifactID(
		"sha256-deadbeef" + strings.Repeat("0", 56)))
	if err != nil {
		t.Fatal(err)
	}
	want := "manifests/de/ad/sha256-deadbeef" + strings.Repeat("0", 56)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestManifestPath_RejectsEmpty(t *testing.T) {
	_, err := blobpath.ManifestPath("")
	if err == nil {
		t.Fatal("expected error on empty id")
	}
}

func TestManifestPath_RejectsMalformedID(t *testing.T) {
	_, err := blobpath.ManifestPath("malformed")
	if err == nil {
		t.Fatal("expected error on id without algo prefix")
	}
}

// --- RefFromPath ---

func TestRefFromPath_Sharded(t *testing.T) {
	got, err := blobpath.RefFromPath(
		"blobs/de/ad/sha256-deadbeef" + strings.Repeat("0", 56))
	if err != nil {
		t.Fatal(err)
	}
	want := "sha256-deadbeef" + strings.Repeat("0", 56)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRefFromPath_Flat(t *testing.T) {
	got, err := blobpath.RefFromPath(
		"blobs/sha256-aabbccdd" + strings.Repeat("0", 56))
	if err != nil {
		t.Fatal(err)
	}
	want := "sha256-aabbccdd" + strings.Repeat("0", 56)
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRefFromPath_RoundTripsResolve(t *testing.T) {
	// The whole point of RefFromPath is being the inverse of
	// Resolve for orphan scanning. Assert the round-trip on both
	// topologies.
	ref := "sha256-cafebabe" + strings.Repeat("f", 56)

	for _, topo := range []domain.PathTopology{
		domain.PathTopologySharded,
		domain.PathTopologyFlat,
	} {
		path, err := blobpath.Resolve(topo, domain.BlobTypeRegular, ref)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", topo, err)
		}
		got, err := blobpath.RefFromPath(path)
		if err != nil {
			t.Fatalf("RefFromPath(%q): %v", path, err)
		}
		if got != ref {
			t.Errorf("topology %s: got %q, want %q", topo, got, ref)
		}
	}
}

func TestRefFromPath_RejectsEmpty(t *testing.T) {
	if _, err := blobpath.RefFromPath(""); err == nil {
		t.Fatal("expected error on empty path")
	}
}

func TestRefFromPath_RejectsMissingAlgoPrefix(t *testing.T) {
	if _, err := blobpath.RefFromPath("blobs/aa/bb/justhexnoalgo"); err == nil {
		t.Fatal("expected error on ref without algo prefix")
	}
}

func TestRefFromPath_RejectsTooShortHex(t *testing.T) {
	if _, err := blobpath.RefFromPath("blobs/aa/bb/sha256-abc"); err == nil {
		t.Fatal("expected error on hex tail shorter than 4 chars")
	}
}

func TestRefFromPath_RejectsNonHexChars(t *testing.T) {
	// "z" is not in [0-9a-f]. The ref-shape validator must reject
	// it — orphan-scan callers rely on this to skip junk files
	// rather than try to Resolve on garbage.
	if _, err := blobpath.RefFromPath("blobs/aa/bb/sha256-zzzz"); err == nil {
		t.Fatal("expected error on non-hex chars in ref")
	}
}

func TestRefFromPath_FlatPathSingleSegment(t *testing.T) {
	// No slashes at all — the path IS the ref itself.
	ref := "sha256-1234" + strings.Repeat("a", 60)
	got, err := blobpath.RefFromPath(ref)
	if err != nil {
		t.Fatal(err)
	}
	if got != ref {
		t.Errorf("got %q, want %q", got, ref)
	}
}

// --- ArtifactIDFromManifestPath ---

func TestArtifactIDFromManifestPath_Basic(t *testing.T) {
	id := domain.ArtifactID("sha256-deadbeef" + strings.Repeat("0", 56))
	path, err := blobpath.ManifestPath(id)
	if err != nil {
		t.Fatalf("ManifestPath: %v", err)
	}
	got, err := blobpath.ArtifactIDFromManifestPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != id {
		t.Errorf("got %q, want %q", got, id)
	}
}

func TestArtifactIDFromManifestPath_RejectsEmpty(t *testing.T) {
	if _, err := blobpath.ArtifactIDFromManifestPath(""); err == nil {
		t.Fatal("expected error on empty path")
	}
}

func TestArtifactIDFromManifestPath_RejectsMalformedSegment(t *testing.T) {
	if _, err := blobpath.ArtifactIDFromManifestPath(
		"manifests/aa/bb/no-algo-just-text"); err == nil {
		t.Fatal("expected error on malformed segment")
	}
}

func TestArtifactIDFromManifestPath_RejectsNonHex(t *testing.T) {
	if _, err := blobpath.ArtifactIDFromManifestPath(
		"manifests/aa/bb/sha256-zzzz"); err == nil {
		t.Fatal("expected error on non-hex tail")
	}
}
