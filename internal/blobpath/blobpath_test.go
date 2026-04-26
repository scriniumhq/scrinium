package blobpath_test

import (
	"strings"
	"testing"

	"github.com/rkurbatov/scrinium/domain"
	"github.com/rkurbatov/scrinium/internal/blobpath"
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
