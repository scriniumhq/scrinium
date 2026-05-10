package projection_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"scrinium.dev/engine/domain"
	"scrinium.dev/engine/projection"
	"scrinium.dev/internal/testutil/projectionfx"
)

// minimalView returns a View built on an empty FakeSource so
// stats tests don't depend on a real backend.
func minimalView(t *testing.T) *projection.View {
	t.Helper()
	src := projectionfx.New()
	v, err := projection.NewView(context.Background(), src,
		projection.WithRootView(projection.RootByPath),
	)
	if err != nil {
		t.Fatalf("NewView: %v", err)
	}
	return v
}

// TestRenderStats_HeaderAndDaemonSection ensures the canonical
// header and the [daemon] block always appear, even when the
// info struct is sparsely populated. Daemons that scrape this
// expect a stable shape.
func TestRenderStats_HeaderAndDaemonSection(t *testing.T) {
	v := minimalView(t)
	out := string(projection.RenderStats(v, projection.DaemonInfo{
		StartedAt: time.Now().Add(-1 * time.Hour),
	}))

	if !strings.HasPrefix(out, "Scrinium projection stats") {
		t.Errorf("missing header: %q", firstLine(out))
	}
	if !strings.Contains(out, "[daemon]") {
		t.Error("missing [daemon] section")
	}
	if !strings.Contains(out, "Source:") {
		t.Error("missing Source row")
	}
	if !strings.Contains(out, "Started:") {
		t.Error("missing Started row")
	}
	if !strings.Contains(out, "Uptime:") {
		t.Error("missing Uptime row")
	}
}

// TestRenderStats_OmitsOptionalFields verifies the renderer
// suppresses sections and rows whose data is missing. Daemons
// without an Editing concept (FUSE, in some configs) shouldn't
// see a stale "Editing:" row in their output.
func TestRenderStats_OmitsOptionalFields(t *testing.T) {
	v := minimalView(t)
	out := string(projection.RenderStats(v, projection.DaemonInfo{
		StartedAt: time.Now(),
		// MountSession, StorePath, Editing, Namespace, Capacity,
		// Extensions intentionally zero.
	}))

	if strings.Contains(out, "MountSession:") {
		t.Error("MountSession row leaked despite empty field")
	}
	if strings.Contains(out, "StorePath:") {
		t.Error("StorePath row leaked despite empty field")
	}
	if strings.Contains(out, "[storage]") {
		t.Error("[storage] section leaked despite nil Capacity")
	}
	if strings.Contains(out, "[extensions]") {
		t.Error("[extensions] section leaked despite nil Extensions")
	}
}

// TestRenderStats_StorageSection covers the [storage] block,
// including the dedup-ratio synthesis and the n/a rendering for
// unavailable Driver capacity.
func TestRenderStats_StorageSection(t *testing.T) {
	v := minimalView(t)
	out := string(projection.RenderStats(v, projection.DaemonInfo{
		StartedAt: time.Now(),
		Capacity: &domain.StorageInfo{
			TotalBytes:     1024 * 1024 * 1024, // 1 GiB
			UsedBytes:      512 * 1024 * 1024,  // 512 MiB
			AvailableBytes: 512 * 1024 * 1024,
			ArtifactCount:  10,
			BlobCount:      4,
		},
	}))
	if !strings.Contains(out, "[storage]") {
		t.Fatal("missing [storage] section")
	}
	if !strings.Contains(out, "ArtifactCount:    10") {
		t.Error("ArtifactCount row malformed")
	}
	if !strings.Contains(out, "BlobCount:        4") {
		t.Error("BlobCount row malformed")
	}
	if !strings.Contains(out, "DedupRatio:       2.50x") {
		t.Errorf("DedupRatio row missing or wrong:\n%s", out)
	}
	if !strings.Contains(out, "GiB") {
		t.Error("byte size humanisation missing")
	}
}

// TestRenderStats_StorageNA verifies "-1" sentinels render as
// "n/a" — Driver-reported "unavailable" must not surface as a
// negative number to the user.
func TestRenderStats_StorageNA(t *testing.T) {
	v := minimalView(t)
	out := string(projection.RenderStats(v, projection.DaemonInfo{
		StartedAt: time.Now(),
		Capacity: &domain.StorageInfo{
			TotalBytes:     -1,
			UsedBytes:      -1,
			AvailableBytes: -1,
			ArtifactCount:  -1,
			BlobCount:      -1,
		},
	}))
	if !strings.Contains(out, "TotalBytes:       n/a") {
		t.Error("-1 not rendered as n/a in TotalBytes")
	}
	if !strings.Contains(out, "ArtifactCount:    n/a") {
		t.Error("-1 not rendered as n/a in ArtifactCount")
	}
}

// TestRenderStats_ExtensionsSection covers [extensions]:
// rendered when non-nil, sorted by name, "(none registered)" for
// empty slice (different from nil!).
func TestRenderStats_ExtensionsSection(t *testing.T) {
	v := minimalView(t)

	t.Run("populated", func(t *testing.T) {
		out := string(projection.RenderStats(v, projection.DaemonInfo{
			StartedAt: time.Now(),
			Extensions: []projection.ExtensionInfo{
				{Name: "scrinium.zeta", SchemaVersion: 2},
				{Name: "scrinium.alpha", SchemaVersion: 1},
			},
		}))
		if !strings.Contains(out, "[extensions]") {
			t.Fatal("missing [extensions]")
		}
		// Alphabetical ordering in output.
		alpha := strings.Index(out, "scrinium.alpha")
		zeta := strings.Index(out, "scrinium.zeta")
		if alpha < 0 || zeta < 0 {
			t.Fatal("extensions missing from output")
		}
		if alpha >= zeta {
			t.Error("extensions not sorted alphabetically")
		}
	})

	t.Run("empty slice shows (none registered)", func(t *testing.T) {
		out := string(projection.RenderStats(v, projection.DaemonInfo{
			StartedAt:  time.Now(),
			Extensions: []projection.ExtensionInfo{},
		}))
		if !strings.Contains(out, "[extensions]") {
			t.Fatal("section missing for empty slice")
		}
		if !strings.Contains(out, "(none registered)") {
			t.Error("empty extensions slice did not render placeholder")
		}
	})
}

// TestRenderStats_ConfigSection covers [config] visibility: the
// section appears only when at least one config knob is set.
func TestRenderStats_ConfigSection(t *testing.T) {
	v := minimalView(t)

	out := string(projection.RenderStats(v, projection.DaemonInfo{
		StartedAt: time.Now(),
		ReadOnly:  true,
		Editing:   "on",
		Namespace: "files",
	}))
	if !strings.Contains(out, "[config]") {
		t.Fatal("missing [config] section")
	}
	if !strings.Contains(out, "ReadOnly:         true") {
		t.Error("ReadOnly row missing")
	}
	if !strings.Contains(out, "Editing:          on") {
		t.Error("Editing row missing")
	}
	if !strings.Contains(out, "Namespace:        files") {
		t.Error("Namespace row missing")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
