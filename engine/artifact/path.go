package artifact

import (
	"fmt"
	"strings"

	"scrinium.dev/domain"
)

// path.go — the driver-side path layout for blobs and manifests.
//
// Pure functions, no I/O. The conventions here are part of the on-disk
// format: changing them requires a migration. Canonical layouts:
//
//	Sharded:   blobs/<aa>/<bb>/<ref>   (aa,bb = hex chars 1..4 of the ref)
//	Flat:      blobs/<full-ref>
//
// Chunk and pack blobs use the same topology with roots "chunks/" and
// "packs/"; the BlobType argument selects the root. Manifests live under
// "manifests/" and are always Sharded — even on object stores the manifest
// directory sees enough churn that two-level sharding pays off. A manifest
// file is named by its ManifestDigest (the hash of the file bytes), NOT by
// the floating ArtifactID (the handle); the index maps handle → digest.

// rootFor returns the directory prefix for a blob type: "blobs",
// "chunks", "packs". An empty type means Regular. Unknown types error —
// callers should validate first, but a defensive check is cheap.
func rootFor(t domain.BlobType) (string, error) {
	switch t {
	case "", domain.BlobTypeRegular:
		return "blobs", nil
	case domain.BlobTypeChunk:
		return "chunks", nil
	case domain.BlobTypePack:
		return "packs", nil
	}
	return "", fmt.Errorf("artifact: unknown blob type %q", t)
}

// shardOf extracts the two two-character shards from a bare-hex ref
// (ADR-93: no "<algo>-" prefix). Hex is case-folded to lowercase so the
// on-disk layout stays stable across hashers that might emit uppercase. A
// ref shorter than four chars errors: such a ref could only come from a
// misconfigured HashRegistry, and short shard names are collision-prone.
func shardOf(ref string) (string, string, error) {
	low := strings.ToLower(ref)
	if len(low) < 4 {
		return "", "", fmt.Errorf("artifact: ref too short to shard: %q", ref)
	}
	return low[0:2], low[2:4], nil
}

// BlobPath returns the driver-side path for a blob with the given ref
// under the given topology and type. The result is forward-slash
// separated and root-relative — exactly what driver.Driver expects.
//
// An empty topology is treated as Sharded (the InitStore default), and an
// empty BlobType as Regular.
func BlobPath(topology domain.PathTopology, blobType domain.BlobType, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("artifact: empty ref")
	}
	root, err := rootFor(blobType)
	if err != nil {
		return "", err
	}
	switch topology {
	case domain.PathTopologyFlat:
		return root + "/" + ref, nil
	case "", domain.PathTopologySharded:
		s1, s2, err := shardOf(ref)
		if err != nil {
			return "", err
		}
		return root + "/" + s1 + "/" + s2 + "/" + ref, nil
	}
	return "", fmt.Errorf("artifact: unknown topology %q", topology)
}

// ManifestPath returns the driver-side path of a manifest file by its
// ManifestDigest. Manifests live under "manifests/" and are always
// Sharded; there is no Flat manifest layout. The path is keyed by digest
// (the current physical form), not by the floating ArtifactID.
func ManifestPath(digest domain.ManifestDigest) (string, error) {
	if digest == "" {
		return "", fmt.Errorf("artifact: empty manifest digest")
	}
	s1, s2, err := shardOf(string(digest))
	if err != nil {
		return "", fmt.Errorf("artifact: manifest %w", err)
	}
	return "manifests/" + s1 + "/" + s2 + "/" + string(digest), nil
}

// RefFromBlobPath extracts the blob ref ("<algo>-<hex>") from a
// driver-side blob path produced by BlobPath. Both topologies are
// supported (the ref is always the last path segment). The check is
// purely structural — last-segment is bare hex (ADR-93) of at least four
// chars. It does NOT cross-check the
// segment against the topology shards; a caller that needs that paranoia
// re-runs BlobPath on the result and compares. Used by the Orphan Scan to
// map a driver-listed file back to a StoreIndex key.
func RefFromBlobPath(p string) (string, error) {
	last, err := lastSegment(p)
	if err != nil {
		return "", err
	}
	if err := validateRefShape(last); err != nil {
		return "", fmt.Errorf("artifact: %w (path %q)", err, p)
	}
	return last, nil
}

// DigestFromManifestPath is the manifests-side counterpart of
// RefFromBlobPath; manifest paths are always Sharded and the structural
// validation is identical. It returns the ManifestDigest (the file's
// physical name), not the handle.
func DigestFromManifestPath(p string) (domain.ManifestDigest, error) {
	last, err := lastSegment(p)
	if err != nil {
		return "", err
	}
	if err := validateRefShape(last); err != nil {
		return "", fmt.Errorf("artifact: manifest %w (path %q)", err, p)
	}
	return domain.ManifestDigest(last), nil
}

// lastSegment returns the final '/'-separated component of p, erroring on
// an empty path.
func lastSegment(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("artifact: empty path")
	}
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:], nil
	}
	return p, nil
}

// validateRefShape checks the bare-hex structural form (ADR-93: no
// "<algo>-" prefix) — at least four chars, all lowercase hex.
func validateRefShape(s string) error {
	if len(s) < 4 {
		return fmt.Errorf("ref %q shorter than 4 chars", s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return fmt.Errorf("ref %q has non-hex char at position %d", s, i)
		}
	}
	return nil
}
