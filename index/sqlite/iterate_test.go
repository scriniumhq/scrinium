package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rkurbatov/scrinium/core"
	"github.com/rkurbatov/scrinium/domain"
)

// insertManifest inserts a manifest row directly via SQL, bypassing
// IndexManifest. Lets list-side tests stay focused on listing
// semantics without going through IndexManifest's blob-side
// bookkeeping.
func insertManifest(t *testing.T, idx *Index, m domain.Manifest) {
	t.Helper()
	createdAt := m.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	// blob_ref is NULL for Inline manifests (§9.1.2). The list
	// helpers in tests rarely set LayoutHeader, so the common
	// path is non-NULL — but we honour the invariant either way.
	var blobRefArg any
	if m.LayoutHeader.BlobStorage == "Inline" {
		blobRefArg = nil
	} else {
		blobRefArg = string(m.BlobRef)
	}
	var retentionArg any
	if !m.RetentionUntil.IsZero() {
		retentionArg = fmtRFC3339(m.RetentionUntil)
	}
	_, err := idx.db.ExecContext(context.Background(),
		`INSERT INTO manifests (
			artifact_id, type, namespace, session_id,
			blob_ref, created_at, retention_until
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(m.ArtifactID), string(m.Type),
		m.Namespace, m.SessionID, blobRefArg,
		fmtRFC3339(createdAt), retentionArg,
	)
	if err != nil {
		t.Fatalf("insertManifest: %v", err)
	}
}

func collectManifests(t *testing.T, idx *Index, ns string) []domain.Manifest {
	t.Helper()
	var got []domain.Manifest
	err := idx.ListByNamespace(context.Background(), ns, func(m domain.Manifest) error {
		got = append(got, m)
		return nil
	})
	if err != nil {
		t.Fatalf("ListByNamespace(%q): %v", ns, err)
	}
	return got
}

// --- ListByNamespace ---

func TestListByNamespace_ExactMatch(t *testing.T) {
	idx := newMemoryIndex(t)
	insertManifest(t, idx, domain.Manifest{ArtifactID: "a1", Type: domain.ManifestTypeBlob, Namespace: "alpha"})
	insertManifest(t, idx, domain.Manifest{ArtifactID: "a2", Type: domain.ManifestTypeBlob, Namespace: "alpha"})
	insertManifest(t, idx, domain.Manifest{ArtifactID: "b1", Type: domain.ManifestTypeBlob, Namespace: "beta"})

	got := collectManifests(t, idx, "alpha")
	if len(got) != 2 {
		t.Fatalf("got %d manifests, want 2", len(got))
	}
	for _, m := range got {
		if m.Namespace != "alpha" {
			t.Errorf("namespace leak: got %q", m.Namespace)
		}
	}
}

func TestListByNamespace_DefaultNamespace(t *testing.T) {
	idx := newMemoryIndex(t)
	insertManifest(t, idx, domain.Manifest{ArtifactID: "no-ns-1", Type: domain.ManifestTypeBlob, Namespace: ""})
	insertManifest(t, idx, domain.Manifest{ArtifactID: "user-ns", Type: domain.ManifestTypeBlob, Namespace: "alpha"})

	got := collectManifests(t, idx, "")
	if len(got) != 1 {
		t.Fatalf("got %d, want 1 (default namespace only)", len(got))
	}
	if got[0].ArtifactID != "no-ns-1" {
		t.Errorf("got %q, want no-ns-1", got[0].ArtifactID)
	}
}

func TestListByNamespace_Wildcard_ExcludesSystem(t *testing.T) {
	idx := newMemoryIndex(t)
	insertManifest(t, idx, domain.Manifest{ArtifactID: "u1", Type: domain.ManifestTypeBlob, Namespace: "alpha"})
	insertManifest(t, idx, domain.Manifest{ArtifactID: "u2", Type: domain.ManifestTypeBlob, Namespace: "beta"})
	insertManifest(t, idx, domain.Manifest{ArtifactID: "s1", Type: domain.ManifestTypeBlob, Namespace: "system.config"})
	insertManifest(t, idx, domain.Manifest{ArtifactID: "s2", Type: domain.ManifestTypeBlob, Namespace: "system.state"})

	got := collectManifests(t, idx, "*")
	if len(got) != 2 {
		t.Fatalf("got %d manifests, want 2", len(got))
	}
	for _, m := range got {
		if strings.HasPrefix(m.Namespace, "system.") {
			t.Errorf("system.* leaked: %s", m.Namespace)
		}
	}
}

func TestListByNamespace_OrderByCreatedAt(t *testing.T) {
	idx := newMemoryIndex(t)
	now := time.Now()
	// Insert in reverse temporal order.
	insertManifest(t, idx, domain.Manifest{
		ArtifactID: "third", Type: domain.ManifestTypeBlob, Namespace: "ns",
		CreatedAt: now.Add(2 * time.Second),
	})
	insertManifest(t, idx, domain.Manifest{
		ArtifactID: "first", Type: domain.ManifestTypeBlob, Namespace: "ns",
		CreatedAt: now,
	})
	insertManifest(t, idx, domain.Manifest{
		ArtifactID: "second", Type: domain.ManifestTypeBlob, Namespace: "ns",
		CreatedAt: now.Add(time.Second),
	})

	got := collectManifests(t, idx, "ns")
	wantOrder := []domain.ArtifactID{"first", "second", "third"}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	for i, m := range got {
		if m.ArtifactID != wantOrder[i] {
			t.Errorf("position %d: got %q, want %q", i, m.ArtifactID, wantOrder[i])
		}
	}
}

func TestListByNamespace_StopWalk(t *testing.T) {
	idx := newMemoryIndex(t)
	for i := 0; i < 5; i++ {
		insertManifest(t, idx, domain.Manifest{
			ArtifactID: domain.ArtifactID(string(rune('a' + i))),
			Type:       domain.ManifestTypeBlob,
			Namespace:  "ns",
		})
	}

	var seen int
	err := idx.ListByNamespace(context.Background(), "ns", func(m domain.Manifest) error {
		seen++
		if seen == 2 {
			return core.ErrStopWalk
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ErrStopWalk should be swallowed, got %v", err)
	}
	if seen != 2 {
		t.Fatalf("expected to stop at 2, saw %d", seen)
	}
}

func TestListByNamespace_CallbackErrorPropagates(t *testing.T) {
	idx := newMemoryIndex(t)
	insertManifest(t, idx, domain.Manifest{ArtifactID: "a1", Type: domain.ManifestTypeBlob, Namespace: "ns"})

	sentinel := errors.New("custom callback error")
	err := idx.ListByNamespace(context.Background(), "ns", func(m domain.Manifest) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel propagated, got %v", err)
	}
}

func TestListByNamespace_PackManifestsExcluded(t *testing.T) {
	idx := newMemoryIndex(t)
	// Even if a pack manifest somehow ends up in manifests (it
	// should not — but defence in depth), ListByNamespace must
	// not surface it.
	insertManifest(t, idx, domain.Manifest{ArtifactID: "blob-1", Type: domain.ManifestTypeBlob, Namespace: "ns"})
	insertManifest(t, idx, domain.Manifest{ArtifactID: "pack-1", Type: domain.ManifestTypePack, Namespace: "ns"})

	got := collectManifests(t, idx, "ns")
	if len(got) != 1 {
		t.Fatalf("got %d, want 1 (pack excluded)", len(got))
	}
	if got[0].Type != domain.ManifestTypeBlob {
		t.Errorf("type: got %q, want blob", got[0].Type)
	}
}

func TestListByNamespace_EmptyResult(t *testing.T) {
	idx := newMemoryIndex(t)
	got := collectManifests(t, idx, "nonexistent-ns")
	if len(got) != 0 {
		t.Fatalf("got %d, want 0", len(got))
	}
}

// TestListByNamespace_FieldsRoundTrip ensures every field we
// persist round-trips through the iterator. The ones we don't
// persist (Pipeline, LayoutHeader, Metadata) stay zero-valued —
// callers reconstruct them from the manifest file on disk.
func TestListByNamespace_FieldsRoundTrip(t *testing.T) {
	idx := newMemoryIndex(t)
	now := time.Now().Truncate(time.Second) // RFC 3339 second precision
	retention := now.Add(time.Hour).Truncate(time.Second)
	src := domain.Manifest{
		ArtifactID:     "art-1",
		Type:           domain.ManifestTypeBlob,
		Namespace:      "ns",
		SessionID:      "sess-42",
		BlobRef:        "blob-1",
		CreatedAt:      now,
		RetentionUntil: retention,
	}
	insertManifest(t, idx, src)

	got := collectManifests(t, idx, "ns")
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	m := got[0]
	if m.ArtifactID != src.ArtifactID {
		t.Errorf("ArtifactID: got %q, want %q", m.ArtifactID, src.ArtifactID)
	}
	if m.Type != src.Type {
		t.Errorf("Type: got %q, want %q", m.Type, src.Type)
	}
	if m.Namespace != src.Namespace {
		t.Errorf("Namespace: got %q, want %q", m.Namespace, src.Namespace)
	}
	if m.SessionID != src.SessionID {
		t.Errorf("SessionID: got %q, want %q", m.SessionID, src.SessionID)
	}
	if m.BlobRef != src.BlobRef {
		t.Errorf("BlobRef: got %q, want %q", m.BlobRef, src.BlobRef)
	}
	if !m.CreatedAt.Equal(src.CreatedAt) {
		t.Errorf("CreatedAt: got %v, want %v", m.CreatedAt, src.CreatedAt)
	}
	if !m.RetentionUntil.Equal(src.RetentionUntil) {
		t.Errorf("RetentionUntil: got %v, want %v", m.RetentionUntil, src.RetentionUntil)
	}
}

// --- GetBySession ---

func TestGetBySession_Hit(t *testing.T) {
	idx := newMemoryIndex(t)
	insertManifest(t, idx, domain.Manifest{ArtifactID: "a1", Type: domain.ManifestTypeBlob, Namespace: "ns", SessionID: "sess-1"})
	insertManifest(t, idx, domain.Manifest{ArtifactID: "a2", Type: domain.ManifestTypeBlob, Namespace: "ns", SessionID: "sess-1"})
	insertManifest(t, idx, domain.Manifest{ArtifactID: "b1", Type: domain.ManifestTypeBlob, Namespace: "ns", SessionID: "sess-2"})

	ids, err := idx.GetBySession("sess-1")
	if err != nil {
		t.Fatalf("GetBySession: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("got %d, want 2", len(ids))
	}
	seen := make(map[domain.ArtifactID]bool)
	for _, id := range ids {
		seen[id] = true
	}
	if !seen["a1"] || !seen["a2"] {
		t.Errorf("missing expected ids: got %v", ids)
	}
}

func TestGetBySession_Miss(t *testing.T) {
	idx := newMemoryIndex(t)
	ids, err := idx.GetBySession("nonexistent")
	if err != nil {
		t.Fatalf("GetBySession: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("got %d, want 0", len(ids))
	}
}

// --- ListOrphanBlobs ---

func TestListOrphanBlobs(t *testing.T) {
	idx := newMemoryIndex(t)
	// Mix of orphan (ref_count=0) and live blobs.
	insertBlob(t, idx, "live-1", "sha256-"+strings.Repeat("a", 64), 1024,
		core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p1"}, 1)
	insertBlob(t, idx, "orphan-1", "sha256-"+strings.Repeat("b", 64), 1024,
		core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p2"}, 0)
	insertBlob(t, idx, "orphan-2", "sha256-"+strings.Repeat("c", 64), 1024,
		core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p3"}, 0)
	insertBlob(t, idx, "live-2", "sha256-"+strings.Repeat("d", 64), 1024,
		core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p4"}, 5)

	var got []string
	err := idx.ListOrphanBlobs(context.Background(), func(ref string) error {
		got = append(got, ref)
		return nil
	})
	if err != nil {
		t.Fatalf("ListOrphanBlobs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	seen := make(map[string]bool)
	for _, ref := range got {
		seen[ref] = true
	}
	if !seen["orphan-1"] || !seen["orphan-2"] {
		t.Errorf("expected both orphans, got %v", got)
	}
}

func TestListOrphanBlobs_StopWalk(t *testing.T) {
	idx := newMemoryIndex(t)
	for i := 0; i < 5; i++ {
		insertBlob(t, idx, string(rune('a'+i)), "sha256-"+strings.Repeat(string(rune('a'+i)), 64), 1024,
			core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p"}, 0)
	}

	var seen int
	err := idx.ListOrphanBlobs(context.Background(), func(ref string) error {
		seen++
		if seen == 2 {
			return core.ErrStopWalk
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ErrStopWalk should be swallowed, got %v", err)
	}
	if seen != 2 {
		t.Fatalf("expected stop at 2, saw %d", seen)
	}
}

// --- ListUnverified ---

func TestListUnverified(t *testing.T) {
	idx := newMemoryIndex(t)
	now := time.Now()

	// "never" — last_verified_at IS NULL after insertBlob. The agent
	// path treats NULL as the highest priority; in the SQL we wrote
	// in iterate.go, NULL rows are matched by `last_verified_at IS NULL`
	// regardless of cutoff.
	insertBlob(t, idx, "never", "sha256-"+strings.Repeat("a", 64), 1024,
		core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p1"}, 1)

	// Verified ten minutes ago: stale per a five-minute cutoff.
	insertBlob(t, idx, "stale", "sha256-"+strings.Repeat("b", 64), 1024,
		core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p2"}, 1)
	tenMinAgo := fmtRFC3339(now.Add(-10 * time.Minute))
	if _, err := idx.db.ExecContext(context.Background(),
		`UPDATE blobs SET last_verified_at = ? WHERE blob_ref = ?`,
		tenMinAgo, "stale",
	); err != nil {
		t.Fatal(err)
	}

	// Verified one minute ago: fresh per the same cutoff.
	insertBlob(t, idx, "fresh", "sha256-"+strings.Repeat("c", 64), 1024,
		core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p3"}, 1)
	oneMinAgo := fmtRFC3339(now.Add(-time.Minute))
	if _, err := idx.db.ExecContext(context.Background(),
		`UPDATE blobs SET last_verified_at = ? WHERE blob_ref = ?`,
		oneMinAgo, "fresh",
	); err != nil {
		t.Fatal(err)
	}

	cutoff := now.Add(-5 * time.Minute)
	var got []string
	err := idx.ListUnverified(context.Background(), cutoff, func(ref string) error {
		got = append(got, ref)
		return nil
	})
	if err != nil {
		t.Fatalf("ListUnverified: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (never+stale)", len(got))
	}
	seen := make(map[string]bool)
	for _, ref := range got {
		seen[ref] = true
	}
	if !seen["never"] {
		t.Error("expected 'never' in the result")
	}
	if !seen["stale"] {
		t.Error("expected 'stale' in the result")
	}
	if seen["fresh"] {
		t.Error("'fresh' leaked through cutoff")
	}
}

func TestListUnverified_OldestFirst(t *testing.T) {
	idx := newMemoryIndex(t)
	now := time.Now()

	// Three blobs, verified at different times in the past.
	insertBlob(t, idx, "older", "sha256-"+strings.Repeat("o", 64), 1024,
		core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p"}, 1)
	insertBlob(t, idx, "middle", "sha256-"+strings.Repeat("m", 64), 1024,
		core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p"}, 1)
	insertBlob(t, idx, "newer", "sha256-"+strings.Repeat("n", 64), 1024,
		core.PhysicalAddress{Workspace: core.WorkspaceLocation, Path: "p"}, 1)

	for ref, ago := range map[string]time.Duration{
		"older":  -3 * time.Hour,
		"middle": -2 * time.Hour,
		"newer":  -1 * time.Hour,
	} {
		ts := fmtRFC3339(now.Add(ago))
		if _, err := idx.db.ExecContext(context.Background(),
			`UPDATE blobs SET last_verified_at = ? WHERE blob_ref = ?`, ts, ref); err != nil {
			t.Fatal(err)
		}
	}

	cutoff := now
	var got []string
	err := idx.ListUnverified(context.Background(), cutoff, func(ref string) error {
		got = append(got, ref)
		return nil
	})
	if err != nil {
		t.Fatalf("ListUnverified: %v", err)
	}
	wantOrder := []string{"older", "middle", "newer"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d, want %d", len(got), len(wantOrder))
	}
	for i, ref := range got {
		if ref != wantOrder[i] {
			t.Errorf("position %d: got %q, want %q", i, ref, wantOrder[i])
		}
	}
}

// --- ContextCancellation smoke test ---

func TestListByNamespace_ContextCancelled(t *testing.T) {
	idx := newMemoryIndex(t)
	for i := 0; i < 3; i++ {
		insertManifest(t, idx, domain.Manifest{
			ArtifactID: domain.ArtifactID(string(rune('a' + i))),
			Type:       domain.ManifestTypeBlob,
			Namespace:  "ns",
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := idx.ListByNamespace(ctx, "ns", func(m domain.Manifest) error {
		return nil
	})
	// Cancellation may be observed either before the query starts
	// (driver returns ctx.Err() from QueryContext) or before the
	// first row is scanned (our loop sees ctx.Err()). Either way
	// errors.Is(err, context.Canceled) must hold.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
