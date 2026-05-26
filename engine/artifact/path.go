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
//	Sharded:   blobs/<aa>/<bb>/<full-ref>   (aa,bb = hex chars 1..4 of the ref hex)
//	Flat:      blobs/<full-ref>
//	Native:    rejected — Native means BlobStorage: ExternalRef and skips
//	           the blob-path machinery entirely.
//
// Chunk and pack blobs use the same topology with roots "chunks/" and
// "packs/"; the BlobType argument selects the root. Manifests live under
// "manifests/" and are always Sharded — even on object stores the manifest
// directory sees enough churn that two-level sharding pays off.

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

// shardOf extracts the two two-character shards from a ref of the form
// "<algo>-<hex>". Hex is case-folded to lowercase so the on-disk layout
// stays stable across hashers that might emit uppercase. A ref whose hex
// tail is shorter than four chars errors: such a ref could only come from
// a misconfigured HashRegistry, and short shard names are collision-prone.
func shardOf(ref string) (string, string, error) {
	dash := strings.IndexByte(ref, '-')
	if dash < 0 {
		return "", "", fmt.Errorf("artifact: ref missing algo prefix: %q", ref)
	}
	hex := strings.ToLower(ref[dash+1:])
	if len(hex) < 4 {
		return "", "", fmt.Errorf("artifact: hex prefix too short in ref %q", ref)
	}
	return hex[0:2], hex[2:4], nil
}

// BlobPath returns the driver-side path for a blob with the given ref
// under the given topology and type. The result is forward-slash
// separated and root-relative — exactly what driver.Driver expects.
//
// PathTopologyNative is rejected: a "native" blob is referenced by
// ExternalRef URI and handled through driver.Open, not the path machinery.
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
	case domain.PathTopologyNative:
		return "", fmt.Errorf("artifact: Native topology has no managed path; use ExternalRef")
	}
	return "", fmt.Errorf("artifact: unknown topology %q", topology)
}

// ManifestPath returns the driver-side path of a manifest file by
// ArtifactID. Manifests live under "manifests/" and are always Sharded;
// there is no Flat manifest layout.
func ManifestPath(id domain.ArtifactID) (string, error) {
	if id == "" {
		return "", fmt.Errorf("artifact: empty artifact id")
	}
	s1, s2, err := shardOf(string(id))
	if err != nil {
		return "", fmt.Errorf("artifact: manifest %w", err)
	}
	return "manifests/" + s1 + "/" + s2 + "/" + string(id), nil
}

// RefFromBlobPath extracts the blob ref ("<algo>-<hex>") from a
// driver-side blob path produced by BlobPath. Both topologies are
// supported (the ref is always the last path segment). The check is
// purely structural — last-segment shape "<algo>-<hex>" with a non-empty
// algo and a hex tail of at least four chars. It does NOT cross-check the
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

// IDFromManifestPath is the manifests-side counterpart of RefFromBlobPath;
// manifest paths are always Sharded and the structural validation is
// identical.
func IDFromManifestPath(p string) (domain.ArtifactID, error) {
	last, err := lastSegment(p)
	if err != nil {
		return "", err
	}
	if err := validateRefShape(last); err != nil {
		return "", fmt.Errorf("artifact: manifest %w (path %q)", err, p)
	}
	return domain.ArtifactID(last), nil
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

// validateRefShape checks the structural form "<algo>-<hex>" with a
// non-empty algo prefix and at least four lowercase hex chars after the
// dash (matching shardOf's lower bound).
func validateRefShape(s string) error {
	dash := strings.IndexByte(s, '-')
	if dash <= 0 {
		return fmt.Errorf("ref %q missing algo prefix", s)
	}
	hex := s[dash+1:]
	if len(hex) < 4 {
		return fmt.Errorf("ref %q has hex tail shorter than 4 chars", s)
	}
	for i := 0; i < len(hex); i++ {
		c := hex[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return fmt.Errorf("ref %q has non-hex char at position %d", s, i)
		}
	}
	return nil
}
