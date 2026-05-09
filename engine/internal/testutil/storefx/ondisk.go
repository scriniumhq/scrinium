// Package storefx — on-disk inspection helpers.
//
// Tests that assert physical layout (manifest path, blob count,
// staging-directory cleanup) reach into the localfs root through
// these helpers instead of hand-rolling filepath.Join /
// filepath.Walk. One change to the on-disk layout (e.g. a new
// PathTopology) propagates through OnDisk; tests stay stable.

package storefx

import (
	"os"
	"path/filepath"
	"testing"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/internal/blobpath"
	"scrinium.dev/engine/internal/manifestcodec"
)

// OnDisk wraps a localfs root for physical inspection. Construct
// via OnDiskAt(root) or via the Reopener.OnDisk() shortcut.
//
// Methods that take *testing.TB call t.Fatalf on unexpected I/O —
// a stat-error means the test's setup is wrong, not the engine's
// behaviour.
type OnDisk struct {
	Root string
}

// OnDiskAt returns an OnDisk inspector rooted at the given path.
func OnDiskAt(root string) OnDisk {
	return OnDisk{Root: root}
}

// ManifestPath returns the on-disk path of the manifest file for
// the given ArtifactID, computed via the same blobpath helper that
// core uses internally. Tests use this to assert manifest presence
// or absence without re-implementing the shard rule.
//
// Returns an empty string if blobpath rejects the ID — callers
// should treat that as a test setup error and fail explicitly.
func (d OnDisk) ManifestPath(id domain.ArtifactID) string {
	rel, err := blobpath.ManifestPath(id)
	if err != nil {
		return ""
	}
	return filepath.Join(d.Root, rel)
}

// ManifestExists reports whether the manifest file for id is
// present on disk. Used after Delete to assert physical removal.
func (d OnDisk) ManifestExists(id domain.ArtifactID) bool {
	p := d.ManifestPath(id)
	if p == "" {
		return false
	}
	_, err := os.Stat(p)
	return err == nil
}

// ReadManifest decodes the manifest file at id. Used to inspect
// fields that Walk does not return (LayoutHeader, InlineBlob,
// Pipeline, Metadata) — the index is a routing layer, not a
// source of truth for manifest content.
//
// Calls t.Fatalf on read or decode failure.
func (d OnDisk) ReadManifest(t testing.TB, id domain.ArtifactID) domain.Manifest {
	t.Helper()
	p := d.ManifestPath(id)
	if p == "" {
		t.Fatalf("storefx.OnDisk.ReadManifest: invalid id %q", id)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("storefx.OnDisk.ReadManifest: read %s: %v", p, err)
	}
	m, err := manifestcodec.DecodeFile(raw)
	if err != nil {
		t.Fatalf("storefx.OnDisk.ReadManifest: decode %s: %v", p, err)
	}
	return m
}

// BlobCount returns the number of regular files under <root>/blobs/.
// A missing blobs/ directory counts as zero.
func (d OnDisk) BlobCount() int {
	var n int
	_ = filepath.Walk(filepath.Join(d.Root, "blobs"),
		func(_ string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				n++
			}
			return nil
		})
	return n
}

// BlobFiles returns the absolute paths of every regular file under
// <root>/blobs/. Order is filepath.Walk-driven (lexicographic).
// A missing blobs/ directory yields an empty slice.
func (d OnDisk) BlobFiles() []string {
	var out []string
	_ = filepath.Walk(filepath.Join(d.Root, "blobs"),
		func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				out = append(out, path)
			}
			return nil
		})
	return out
}

// StagingFiles returns regular files under
// <root>/system.state/staging/. A non-empty result after a
// completed operation indicates a leak.
func (d OnDisk) StagingFiles() []string {
	dir := filepath.Join(d.Root, filepath.FromSlash(domain.NamespaceSystemState), "staging")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}
