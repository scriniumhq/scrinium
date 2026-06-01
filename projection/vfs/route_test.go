package vfs

import (
	"errors"
	"testing"

	"scrinium.dev/projection/internal/view"
)

func defaultRoutingConfig() Config {
	return Config{
		ServicePrefix:   "_scrinium",
		ShowStats:       true,
		ShowByArtifact:  true,
		ShowOrphaned:    true,
		ShowByDate:      true,
		ShowBySession:   true,
		ShowByNamespace: true,
		ShowRaw:         true,
	}
}

func TestRoute_MountRoot(t *testing.T) {
	r, err := route("", defaultRoutingConfig(), view.RootByPath)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Kind != kindRoot {
		t.Errorf("Kind: got %v, want kindRoot", r.Kind)
	}
	if r.Tree != view.RootByPath {
		t.Errorf("Tree: got %v, want by-path", r.Tree)
	}
}

func TestRoute_RegularPath(t *testing.T) {
	r, err := route("photos/img.jpg", defaultRoutingConfig(), view.RootByPath)
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
	// rootView is now supplied separately (derived from the View),
	// not carried in Config.
	r, _ := route("2024/05/03/14-23-05-aabb.bin", defaultRoutingConfig(), view.RootByDate)
	if r.Tree != view.RootByDate {
		t.Errorf("Tree: got %v, want by-date", r.Tree)
	}
}

func TestRoute_ServiceRoot(t *testing.T) {
	r, err := route("_scrinium", defaultRoutingConfig(), view.RootByPath)
	if err != nil {
		t.Fatalf("route: %v", err)
	}
	if r.Kind != kindServiceRoot {
		t.Errorf("Kind: got %v, want kindServiceRoot", r.Kind)
	}
}

func TestRoute_ServiceTree_BySession(t *testing.T) {
	r, _ := route("_scrinium/by-session/ab/cd/sid/aid", defaultRoutingConfig(), view.RootByPath)
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
	r, _ := route("_scrinium/by-session", defaultRoutingConfig(), view.RootByPath)
	if r.Kind != kindServiceTree {
		t.Fatalf("Kind: got %v", r.Kind)
	}
	if r.SubPath != "" {
		t.Errorf("SubPath: got %q, want empty (tree root)", r.SubPath)
	}
}

func TestRoute_StatsFile(t *testing.T) {
	r, _ := route("_scrinium/stats", defaultRoutingConfig(), view.RootByPath)
	if r.Kind != kindStatsFile {
		t.Errorf("Kind: got %v, want kindStatsFile", r.Kind)
	}
}

func TestRoute_StatsWithSubPath_Rejected(t *testing.T) {
	_, err := route("_scrinium/stats/garbage", defaultRoutingConfig(), view.RootByPath)
	if !errors.Is(err, errRejected) {
		t.Errorf("expected errRejected, got %v", err)
	}
}

func TestRoute_RawMirror(t *testing.T) {
	r, _ := route("_scrinium/raw/blobs/sha256/aa/bb/file", defaultRoutingConfig(), view.RootByPath)
	if r.Kind != kindRawMirror {
		t.Errorf("Kind: got %v, want kindRawMirror", r.Kind)
	}
	if r.RawSubPath != "blobs/sha256/aa/bb/file" {
		t.Errorf("RawSubPath: got %q", r.RawSubPath)
	}
}

func TestRoute_RawDisabled(t *testing.T) {
	cfg := defaultRoutingConfig()
	cfg.ShowRaw = false
	_, err := route("_scrinium/raw/anything", cfg, view.RootByPath)
	if !errors.Is(err, errRejected) {
		t.Errorf("expected errRejected, got %v", err)
	}
}

func TestRoute_DisabledTree(t *testing.T) {
	cfg := defaultRoutingConfig()
	cfg.ShowBySession = false
	_, err := route("_scrinium/by-session/anything", cfg, view.RootByPath)
	if !errors.Is(err, errRejected) {
		t.Errorf("expected errRejected, got %v", err)
	}
}

func TestRoute_UnknownTree(t *testing.T) {
	_, err := route("_scrinium/by-bogus/x", defaultRoutingConfig(), view.RootByPath)
	if !errors.Is(err, errRejected) {
		t.Errorf("expected errRejected, got %v", err)
	}
}

func TestRoute_ServicePrefixDisabled(t *testing.T) {
	// Empty prefix → every path routes to kindRoot, including
	// what would otherwise be a service path.
	cfg := defaultRoutingConfig()
	cfg.ServicePrefix = ""
	r, _ := route("_scrinium/by-session/anything", cfg, view.RootByPath)
	if r.Kind != kindRoot {
		t.Errorf("Kind: got %v, want kindRoot", r.Kind)
	}
	if r.SubPath != "_scrinium/by-session/anything" {
		t.Errorf("SubPath: got %q", r.SubPath)
	}
}

func TestRoute_ServicePrefixOnlyAtRoot(t *testing.T) {
	// "_scrinium" deeper in the path is a regular component.
	cfg := defaultRoutingConfig()
	r, _ := route("photos/_scrinium/img.jpg", cfg, view.RootByPath)
	if r.Kind != kindRoot {
		t.Errorf("Kind: got %v, want kindRoot", r.Kind)
	}
	if r.SubPath != "photos/_scrinium/img.jpg" {
		t.Errorf("SubPath: got %q", r.SubPath)
	}
}

func TestIsServicePath(t *testing.T) {
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
		if got := isServicePath(tc.path, cfg); got != tc.want {
			t.Errorf("isServicePath(%q): got %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsServicePath_PrefixDisabled(t *testing.T) {
	cfg := defaultRoutingConfig()
	cfg.ServicePrefix = ""
	if isServicePath("_scrinium", cfg) {
		t.Error("with empty prefix, isServicePath must always be false")
	}
}
