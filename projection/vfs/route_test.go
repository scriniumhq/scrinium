package vfs

import (
	"errors"
	"testing"

	"scrinium.dev/projection/internal/view"
)

func defaultRoutingConfig() Config {
	return Config{
		ServicePrefix:     "_scrinium",
		ShowStats:         true,
		ShowByArtifact:    true,
		ShowOrphaned:      true,
		ShowByDate:        true,
		ShowBySession:     true,
		ShowProvidedViews: true,
		ShowRaw:           true,
	}
}

func TestRoute_MountRoot(t *testing.T) {
	t.Parallel()
	r, err := route("", defaultRoutingConfig(), view.RootView("by-path"), nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Kind != kindRoot {
		t.Errorf("Kind: got %v, want kindRoot", r.Kind)
	}
	if r.Tree != view.RootView("by-path") {
		t.Errorf("Tree: got %v, want by-path", r.Tree)
	}
}

func TestRoute_RegularPath(t *testing.T) {
	t.Parallel()
	r, err := route("photos/img.jpg", defaultRoutingConfig(), view.RootView("by-path"), nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Kind != kindRoot {
		t.Errorf("Kind: got %v", r.Kind)
	}
	if r.SubPath != "photos/img.jpg" {
		t.Errorf("SubPath: got %q", r.SubPath)
	}
}

func TestRoute_RootViewByDate(t *testing.T) {
	t.Parallel()
	// rootView is now supplied separately (derived from the View),
	// not carried in Config.
	r, _ := route("2024/05/03/14-23-05-aabb.bin", defaultRoutingConfig(), view.RootByDate, nil)
	if r.Tree != view.RootByDate {
		t.Errorf("Tree: got %v, want by-date", r.Tree)
	}
}

func TestRoute_ServiceRoot(t *testing.T) {
	t.Parallel()
	r, err := route("_scrinium", defaultRoutingConfig(), view.RootView("by-path"), nil)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Kind != kindServiceRoot {
		t.Errorf("Kind: got %v, want kindServiceRoot", r.Kind)
	}
}

func TestRoute_ServiceTree_BySession(t *testing.T) {
	t.Parallel()
	r, _ := route("_scrinium/by-session/ab/cd/sid/aid", defaultRoutingConfig(), view.RootView("by-path"), nil)
	if r.Kind != kindServiceTree {
		t.Errorf("Kind: got %v, want kindServiceTree", r.Kind)
	}
	if r.Tree != view.RootBySession {
		t.Errorf("Tree: got %v, want by-session", r.Tree)
	}
	if r.SubPath != "ab/cd/sid/aid" {
		t.Errorf("SubPath: got %q", r.SubPath)
	}
}

func TestRoute_ServiceTreeRoot(t *testing.T) {
	t.Parallel()
	r, _ := route("_scrinium/by-session", defaultRoutingConfig(), view.RootView("by-path"), nil)
	if r.Kind != kindServiceTree {
		t.Fatalf("Kind: got %v", r.Kind)
	}
	if r.SubPath != "" {
		t.Errorf("SubPath: got %q, want empty (tree root)", r.SubPath)
	}
}

func TestRoute_StatsFile(t *testing.T) {
	t.Parallel()
	r, _ := route("_scrinium/stats", defaultRoutingConfig(), view.RootView("by-path"), nil)
	if r.Kind != kindStatsFile {
		t.Errorf("Kind: got %v, want kindStatsFile", r.Kind)
	}
}

func TestRoute_RawMirror(t *testing.T) {
	t.Parallel()
	r, _ := route("_scrinium/raw/blobs/sha256/aa/bb/file", defaultRoutingConfig(), view.RootView("by-path"), nil)
	if r.Kind != kindRawMirror {
		t.Errorf("Kind: got %v, want kindRawMirror", r.Kind)
	}
	if r.RawSubPath != "blobs/sha256/aa/bb/file" {
		t.Errorf("RawSubPath: got %q", r.RawSubPath)
	}
}

func TestRoute_ServicePrefixDisabled(t *testing.T) {
	t.Parallel()
	// Empty prefix → every path routes to kindRoot, including
	// what would otherwise be a service path.
	cfg := defaultRoutingConfig()
	cfg.ServicePrefix = ""
	r, _ := route("_scrinium/by-session/anything", cfg, view.RootView("by-path"), nil)
	if r.Kind != kindRoot {
		t.Errorf("Kind: got %v, want kindRoot", r.Kind)
	}
	if r.SubPath != "_scrinium/by-session/anything" {
		t.Errorf("SubPath: got %q", r.SubPath)
	}
}

func TestRoute_ServicePrefixOnlyAtRoot(t *testing.T) {
	t.Parallel()
	// "_scrinium" deeper in the path is a regular component.
	cfg := defaultRoutingConfig()
	r, _ := route("photos/_scrinium/img.jpg", cfg, view.RootView("by-path"), nil)
	if r.Kind != kindRoot {
		t.Errorf("Kind: got %v, want kindRoot", r.Kind)
	}
	if r.SubPath != "photos/_scrinium/img.jpg" {
		t.Errorf("SubPath: got %q", r.SubPath)
	}
}

// TestRoute_Rejected collects the paths route must reject with
// errRejected: a stats file with trailing junk, the raw mirror when
// disabled, a disabled service tree, and an unknown service tree.
func TestRoute_Rejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		mod  func(*Config) // nil = default config
	}{
		{"stats with subpath", "_scrinium/stats/garbage", nil},
		{"raw mirror disabled", "_scrinium/raw/anything", func(c *Config) { c.ShowRaw = false }},
		{"disabled tree", "_scrinium/by-session/anything", func(c *Config) { c.ShowBySession = false }},
		{"unknown tree", "_scrinium/by-bogus/x", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := defaultRoutingConfig()
			if tc.mod != nil {
				tc.mod(&cfg)
			}
			if _, err := route(tc.path, cfg, view.RootView("by-path"), nil); !errors.Is(err, errRejected) {
				t.Errorf("route(%q): got %v, want errRejected", tc.path, err)
			}
		})
	}
}

func TestIsServicePath(t *testing.T) {
	t.Parallel()
	cfg := defaultRoutingConfig()
	cases := []struct {
		path string
		want bool
	}{
		{"", false},
		{"photos/img.jpg", false},
		{"_scrinium", true},
		{"_scrinium/anything", true},
		{"photos/_scrinium/img.jpg", false},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if got := isServicePath(tc.path, cfg); got != tc.want {
				t.Errorf("isServicePath(%q): got %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestIsServicePath_PrefixDisabled(t *testing.T) {
	t.Parallel()
	cfg := defaultRoutingConfig()
	cfg.ServicePrefix = ""
	if isServicePath("_scrinium", cfg) {
		t.Error("with empty prefix, isServicePath must always be false")
	}
}
