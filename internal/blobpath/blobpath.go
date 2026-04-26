// Package blobpath maps a blob's logical name to its driver-side
// path according to the Store's PathTopology. Pure functions, no
// I/O, no side effects.
//
// Conventions encoded here are part of the on-disk format: changing
// them requires a migration. The canonical layouts:
//
//	Sharded:   blobs/<aa>/<bb>/<full-ref>
//	           where aa,bb are hex chars 4..6 of the blob's hash hex.
//	Flat:      blobs/<full-ref>
//	Native:    rejected — Native means BlobStorage: ExternalRef
//	           and skips the blob-path machinery entirely.
//
// Chunk and pack paths use the same topology with different roots:
// "chunks/" and "packs/" instead of "blobs/". The BlobType argument
// selects the root.
//
// DAG: blobpath imports only domain. It does not import core,
// driver, or any sibling helper.
package blobpath

import (
	"fmt"
	"strings"

	"github.com/rkurbatov/scrinium/domain"
)

// rootFor returns the directory prefix for a given blob type:
// "blobs/", "chunks/", "packs/". Unknown types return an error —
// callers should validate before reaching this function, but a
// defensive check is cheap.
func rootFor(t domain.BlobType) (string, error) {
	switch t {
	case "", domain.BlobTypeRegular:
		return "blobs", nil
	case domain.BlobTypeChunk:
		return "chunks", nil
	case domain.BlobTypePack:
		return "packs", nil
	}
	return "", fmt.Errorf("blobpath: unknown blob type %q", t)
}

// shardOf extracts the two two-character shards from a blob ref.
// The blob ref format is "<algo>-<hex>"; the first hex chars after
// the dash are taken as the shards. Hex digits are case-folded to
// lowercase to keep the on-disk layout stable across hashers that
// emit upper-case (none currently, but a defensive choice).
//
// A ref shorter than "x-aaaa" returns an error: it cannot be safely
// sharded because the prefix is too short. Such refs would only
// come from a misconfigured HashRegistry; surfacing the error here
// is friendlier than producing collision-prone short directory
// names.
func shardOf(ref string) (string, string, error) {
	dash := strings.IndexByte(ref, '-')
	if dash < 0 {
		return "", "", fmt.Errorf("blobpath: ref missing algo prefix: %q", ref)
	}
	hex := strings.ToLower(ref[dash+1:])
	if len(hex) < 4 {
		return "", "", fmt.Errorf("blobpath: hex prefix too short in ref %q", ref)
	}
	return hex[0:2], hex[2:4], nil
}

// Resolve returns the driver-side path for a blob with the given
// ref under the given topology and type. The result is forward-
// slash separated, root-relative — exactly what driver.Driver
// expects.
//
// PathTopologyNative is rejected: blobs whose physical placement
// is "native" do not go through this resolver — they are referenced
// by ExternalRef URI instead, and the engine handles them through
// driver.Open rather than driver.Get/Put.
func Resolve(topology domain.PathTopology, blobType domain.BlobType, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("blobpath: empty ref")
	}
	root, err := rootFor(blobType)
	if err != nil {
		return "", err
	}
	switch topology {
	case domain.PathTopologyFlat:
		return root + "/" + ref, nil
	case "", domain.PathTopologySharded:
		// Empty topology is treated as Sharded. applyConfigDefaults
		// fills it in at InitStore; the empty case here is a
		// belt-and-braces guard for callers who somehow bypass
		// defaults.
		s1, s2, err := shardOf(ref)
		if err != nil {
			return "", err
		}
		return root + "/" + s1 + "/" + s2 + "/" + ref, nil
	case domain.PathTopologyNative:
		return "", fmt.Errorf("blobpath: Native topology has no managed path; use ExternalRef")
	}
	return "", fmt.Errorf("blobpath: unknown topology %q", topology)
}

// ManifestPath returns the driver-side path of a manifest file by
// ArtifactID. Manifests live under "manifests/" and follow the
// same shard rules as blobs — same fan-out concerns apply. There
// is no Flat manifest layout: even on object stores the manifest
// directory sees enough churn that two-level sharding pays off.
func ManifestPath(id domain.ArtifactID) (string, error) {
	if id == "" {
		return "", fmt.Errorf("blobpath: empty artifact id")
	}
	s1, s2, err := shardOf(string(id))
	if err != nil {
		return "", fmt.Errorf("blobpath: manifest %w", err)
	}
	return "manifests/" + s1 + "/" + s2 + "/" + string(id), nil
}
