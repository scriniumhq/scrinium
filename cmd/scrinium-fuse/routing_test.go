package main

import (
	"errors"
	"testing"

	"github.com/rkurbatov/scrinium/projection"
)

func defaultRoutingConfig() RoutingConfig {
	return RoutingConfig{
		ServicePrefix:   "_scrinium",
		RootView:        projection.RootByPath,
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
	r, err := Route("", defaultRoutingConfig())
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if r.Kind != RouteRoot {
		t.Errorf("Kind: got %v, want RouteRoot", r.Kind)
	}
	if r.Tree != projection.RootByPath {
		t.Errorf("Tree: got %v, want by-path", r.Tree)
	}
}

func TestRoute_RegularPath(t *testing.T) {
	r, err := Route("photos/img.jpg", defaultRoutingConfig())
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if r.Kind != RouteRoot {
		t.Errorf("Kind: got %v", r.Kind)
	}
	if r.SubPath != "photos/img.jpg" {
		t.Errorf("SubPath: got %q", r.SubPath)
	}
}

func TestRoute_RootViewByDate(t *testing.T) {
	cfg := defaultRoutingConfig()
	cfg.RootView = projection.RootByDate
	r, _ := Route("2024/05/03/14-23-05-aabb.bin", cfg)
	if r.Tree != projection.RootByDate {
		t.Errorf("Tree: got %v, want by-date", r.Tree)
	}
}

func TestRoute_ServiceRoot(t *testing.T) {
	r, err := Route("_scrinium", defaultRoutingConfig())
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if r.Kind != RouteServiceRoot {
		t.Errorf("Kind: got %v, want RouteServiceRoot", r.Kind)
	}
}

func TestRoute_ServiceTree_BySession(t *testing.T) {
	r, _ := Route("_scrinium/by-session/ab/cd/sid/aid", defaultRoutingConfig())
	if r.Kind != RouteServiceTree {
		t.Errorf("Kind: got %v, want RouteServiceTree", r.Kind)
	}
	if r.Tree != projection.RootBySession {
		t.Errorf("Tree: got %v, want by-session", r.Tree)
	}
	if r.SubPath != "ab/cd/sid/aid" {
		t.Errorf("SubPath: got %q", r.SubPath)
	}
}

func TestRoute_ServiceTreeRoot(t *testing.T) {
	r, _ := Route("_scrinium/by-session", defaultRoutingConfig())
	if r.Kind != RouteServiceTree {
		t.Fatalf("Kind: got %v", r.Kind)
	}
	if r.SubPath != "" {
		t.Errorf("SubPath: got %q, want empty (tree root)", r.SubPath)
	}
}

func TestRoute_StatsFile(t *testing.T) {
	r, _ := Route("_scrinium/stats", defaultRoutingConfig())
	if r.Kind != RouteStatsFile {
		t.Errorf("Kind: got %v, want RouteStatsFile", r.Kind)
	}
}

func TestRoute_StatsWithSubPath_Rejected(t *testing.T) {
	_, err := Route("_scrinium/stats/garbage", defaultRoutingConfig())
	if !errors.Is(err, ErrRouteRejected) {
		t.Errorf("expected ErrRouteRejected, got %v", err)
	}
}

func TestRoute_RawMirror(t *testing.T) {
	r, _ := Route("_scrinium/raw/blobs/sha256/aa/bb/file", defaultRoutingConfig())
	if r.Kind != RouteRawMirror {
		t.Errorf("Kind: got %v, want RouteRawMirror", r.Kind)
	}
	if r.RawSubPath != "blobs/sha256/aa/bb/file" {
		t.Errorf("RawSubPath: got %q", r.RawSubPath)
	}
}

func TestRoute_RawDisabled(t *testing.T) {
	cfg := defaultRoutingConfig()
	cfg.ShowRaw = false
	_, err := Route("_scrinium/raw/anything", cfg)
	if !errors.Is(err, ErrRouteRejected) {
		t.Errorf("expected ErrRouteRejected, got %v", err)
	}
}

func TestRoute_DisabledTree(t *testing.T) {
	cfg := defaultRoutingConfig()
	cfg.ShowBySession = false
	_, err := Route("_scrinium/by-session/anything", cfg)
	if !errors.Is(err, ErrRouteRejected) {
		t.Errorf("expected ErrRouteRejected, got %v", err)
	}
}

func TestRoute_UnknownTree(t *testing.T) {
	_, err := Route("_scrinium/by-bogus/x", defaultRoutingConfig())
	if !errors.Is(err, ErrRouteRejected) {
		t.Errorf("expected ErrRouteRejected, got %v", err)
	}
}

func TestRoute_ServicePrefixDisabled(t *testing.T) {
	// Empty prefix → every path routes to RouteRoot, including
	// what would otherwise be a service path.
	cfg := defaultRoutingConfig()
	cfg.ServicePrefix = ""
	r, _ := Route("_scrinium/by-session/anything", cfg)
	if r.Kind != RouteRoot {
		t.Errorf("Kind: got %v, want RouteRoot", r.Kind)
	}
	if r.SubPath != "_scrinium/by-session/anything" {
		t.Errorf("SubPath: got %q", r.SubPath)
	}
}

func TestRoute_ServicePrefixOnlyAtRoot(t *testing.T) {
	// "_scrinium" deeper in the path is a regular component.
	cfg := defaultRoutingConfig()
	r, _ := Route("photos/_scrinium/img.jpg", cfg)
	if r.Kind != RouteRoot {
		t.Errorf("Kind: got %v, want RouteRoot", r.Kind)
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
		if got := IsServicePath(tc.path, cfg); got != tc.want {
			t.Errorf("IsServicePath(%q): got %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsServicePath_PrefixDisabled(t *testing.T) {
	cfg := defaultRoutingConfig()
	cfg.ServicePrefix = ""
	if IsServicePath("_scrinium", cfg) {
		t.Error("with empty prefix, IsServicePath must always be false")
	}
}
