// Package storekit holds black-box assertion and inspection helpers for
// tests that drive a Store (or its system plane) through the public API.
// It complements testutil/storefx, which constructs Stores: storefx makes
// the subject, storekit interrogates it. Helpers take *testing.T and fail
// the test on error, so call sites stay a single line.
package storekit

import (
	"context"
	"io"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/artifact"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/systemstore"
)

// MustDigest returns the manifest digest of id, failing the test if the
// artifact cannot be read.
func MustDigest(t *testing.T, s store.Store, id domain.ArtifactID) domain.ManifestDigest {
	t.Helper()
	rh, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("MustDigest: Get %s: %v", id, err)
	}
	defer rh.Close()
	return rh.Manifest().Digest
}

// GetBytes reads the full payload of id, failing the test on any error.
func GetBytes(t *testing.T, s store.Store, id domain.ArtifactID) []byte {
	t.Helper()
	rh, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("GetBytes: Get(%s): %v", id, err)
	}
	defer rh.Close()
	got, err := io.ReadAll(rh)
	if err != nil {
		t.Fatalf("GetBytes: ReadAll(%s): %v", id, err)
	}
	return got
}

// ReadBlobRef returns the primary blob ref recorded in id's manifest.
func ReadBlobRef(t *testing.T, s store.Store, id domain.ArtifactID) domain.BlobRef {
	t.Helper()
	rh, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("ReadBlobRef: Get: %v", err)
	}
	defer rh.Close()
	return rh.Manifest().PrimaryBlobRef()
}

// WalkIDs returns the set of artifact IDs visible to a full Store walk.
func WalkIDs(t *testing.T, s store.Store) map[domain.ArtifactID]struct{} {
	t.Helper()
	out := make(map[domain.ArtifactID]struct{})
	err := s.Walk(context.Background(), func(m domain.Manifest) error {
		out[m.ArtifactID] = struct{}{}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkIDs: Walk: %v", err)
	}
	return out
}

// WalkCount returns the number of artifacts visited by a full Store walk.
func WalkCount(t *testing.T, s store.Store) int {
	t.Helper()
	n := 0
	if err := s.Walk(context.Background(), func(domain.Manifest) error {
		n++
		return nil
	}); err != nil {
		t.Fatalf("WalkCount: Walk: %v", err)
	}
	return n
}

// WalkNames returns the names visited by a system-plane walk under prefix.
func WalkNames(t *testing.T, ss systemstore.Store, prefix string) []string {
	t.Helper()
	var names []string
	err := ss.Walk(context.Background(), prefix, func(name string, _ domain.Manifest) error {
		names = append(names, name)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkNames: Walk(%q): %v", prefix, err)
	}
	return names
}

// BlobPathForRef computes the on-disk path for a regular blob ref under
// the sharded topology — the layout tests assert against.
func BlobPathForRef(t *testing.T, ref string) string {
	t.Helper()
	p, err := artifact.BlobPath(domain.PathTopologySharded, domain.BlobTypeRegular, ref)
	if err != nil {
		t.Fatalf("BlobPathForRef: artifact.BlobPath(%q): %v", ref, err)
	}
	return p
}
