package stats_test

import (
	"strings"
	"testing"
	"time"

	"scrinium.dev/domain"
	"scrinium.dev/internal/stats"
	"scrinium.dev/projection"
)

// These cover stats.Render directly with a zero ViewStats plus an
// explicit Source label — no View construction needed, since the
// renderer takes the counters as data. Ported from the projection
// package when the report moved out of the primitive.

func TestRender_HeaderAndDaemonSection(t *testing.T) {
	out := string(stats.Render(projection.ViewStats{}, stats.DaemonInfo{
		Source:    "fake",
		StartedAt: time.Now().Add(-1 * time.Hour),
	}))

	if !strings.HasPrefix(out, "Scrinium projection stats") {
		t.Errorf("missing header: %q", out)
	}
	for _, want := range []string{"[daemon]", "Source:", "Started:", "Uptime:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestRender_OmitsOptionalFields(t *testing.T) {
	out := string(stats.Render(projection.ViewStats{}, stats.DaemonInfo{
		Source:    "fake",
		StartedAt: time.Now(),
	}))

	for _, leaked := range []string{"MountSession:", "StorePath:", "[storage]", "[extensions]"} {
		if strings.Contains(out, leaked) {
			t.Errorf("%q leaked despite empty field", leaked)
		}
	}
}

func TestRender_StorageSection(t *testing.T) {
	out := string(stats.Render(projection.ViewStats{}, stats.DaemonInfo{
		StartedAt: time.Now(),
		Capacity: &domain.StorageInfo{
			TotalBytes:     1024 * 1024 * 1024,
			UsedBytes:      512 * 1024 * 1024,
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

func TestRender_StorageNA(t *testing.T) {
	out := string(stats.Render(projection.ViewStats{}, stats.DaemonInfo{
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

func TestRender_ExtensionsSection(t *testing.T) {
	t.Run("populated", func(t *testing.T) {
		out := string(stats.Render(projection.ViewStats{}, stats.DaemonInfo{
			StartedAt: time.Now(),
			Extensions: []stats.Extension{
				{Name: "scrinium.zeta", SchemaVersion: 2},
				{Name: "scrinium.alpha", SchemaVersion: 1},
			},
		}))
		if !strings.Contains(out, "[extensions]") {
			t.Fatal("missing [extensions]")
		}
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
		out := string(stats.Render(projection.ViewStats{}, stats.DaemonInfo{
			StartedAt:  time.Now(),
			Extensions: []stats.Extension{},
		}))
		if !strings.Contains(out, "[extensions]") {
			t.Fatal("section missing for empty slice")
		}
		if !strings.Contains(out, "(none registered)") {
			t.Error("empty extensions slice did not render placeholder")
		}
	})
}

func TestRender_ConfigSection(t *testing.T) {
	out := string(stats.Render(projection.ViewStats{}, stats.DaemonInfo{
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
