package layout_test

import (
	"strings"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/layout"
)

// FuzzPathParsers hardens the on-disk path parsers against arbitrary
// path strings. Contract (TESTING.md category 1): neither
// RefFromBlobPath nor DigestFromManifestPath may panic on any input;
// malformed paths must be rejected with an error. Return values on
// error are unspecified, so we only assert the no-panic contract here.
//
// Seeds include real ManifestPath output so the fuzzer explores
// near-valid shapes around the shard/segment structure.
func FuzzPathParsers(f *testing.F) {
	if p, err := layout.ManifestPath(domain.ManifestDigest("sha256-" + strings.Repeat("a", 64))); err == nil {
		f.Add(p)
	}
	f.Add("")
	f.Add("/")
	f.Add("a/b/c")
	f.Add("manifests/ab/cd/sha256-" + strings.Repeat("0", 64))
	f.Add("blobs/regular/ab/cd/sha256-deadbeef")
	f.Add(strings.Repeat("../", 32))

	f.Fuzz(func(t *testing.T, p string) {
		_, _ = layout.RefFromBlobPath(p)
		_, _ = layout.DigestFromManifestPath(p)
	})
}
