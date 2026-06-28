package web

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

// StatsData is the structured snapshot the daemon hands the
// web package every time the stats page is rendered. Mirrors
// the sections projection.RenderStats writes to the textual
// _scrinium/stats endpoint, but kept here as plain Go fields
// so the HTML template can lay them out without parsing
// strings.
//
// Empty groups are skipped automatically by the template via
// the IsZero/HasX flags on the embedded structs — the daemon
// passes zero values when a section has no meaningful data.
type StatsData struct {
	Daemon          StatsDaemon
	View            StatsView
	Storage         StatsStorage
	HasStorage      bool
	Extensions      []StatsExtension
	SystemArtifacts []StatsSystemArtifact
	Config          StatsConfig
	HasConfig       bool
}

// StatsDaemon mirrors projection.DaemonInfo's daemon-level
// fields. We redeclare instead of importing projection.DaemonInfo
// directly to keep web a clean schema-agnostic library — the
// daemon translates from its own state at call time.
type StatsDaemon struct {
	Source       string
	StartedAt    time.Time
	Uptime       string
	MountSession string
	StorePath    string
}

// StatsView mirrors view.Stats. ByStore is rendered
// as a sorted list inside the template; the daemon hands the
// map verbatim.
type StatsView struct {
	TotalNodes     int64
	TotalBytes     int64
	TotalBytesText string // pre-formatted "39359055 (37.5 MiB)"
	SessionCount   int64
	ViewCounts     map[string]int64
	OrphanedCount  int64
	CollisionCount int64
	TransitCount   int64
	ByStore        map[string]int64
}

// StatsStorage mirrors domain.StorageInfo. Strings are pre-
// formatted by the daemon ("n/a" for -1 sentinels) so the
// template stays free of value-aware logic.
type StatsStorage struct {
	ArtifactCount  string
	BlobCount      string
	DedupRatio     string // empty when not computable
	TotalBytes     string
	UsedBytes      string
	AvailableBytes string
}

// StatsExtension is one row of the [extensions] section.
type StatsExtension struct {
	Name string
}

// StatsSystemArtifact is one row of the [system artifacts] section:
// a service artifact's active version as reported by SystemStore.Walk.
// Strings are pre-formatted by the daemon (size humanised). The manifest
// digest is omitted — the walk-loaded system manifest does not carry one.
type StatsSystemArtifact struct {
	Name    string
	Size    string
	Created string
}

// StatsConfig mirrors the daemon's policy switches. Boolean
// fields use Go's native rendering ({{.X}} → "true"/"false");
// the template hides the section entirely via HasConfig.
type StatsConfig struct {
	ReadOnly bool
	Editing  string // empty hides the row
}

// StatsProvider is the host-supplied function the stats page
// consults on every request. Returns a fresh snapshot each
// call — counters update live, the daemon is responsible for
// any caching it deems appropriate.
type StatsProvider func() StatsData

// SetStatsProvider installs (or replaces) the daemon-side stats
// snapshot function. Without one, /_stats returns 404 — the
// page is opt-in.
func (h *Handler) SetStatsProvider(p StatsProvider) {
	h.statsProvider = p
}

// serveStats renders the HTML stats page. The provider's
// return value is the full state of the page; we only frame it
// in HTML.
func (h *Handler) serveStats(w http.ResponseWriter, r *http.Request) {
	if h.statsProvider == nil {
		http.NotFound(w, r)
		return
	}
	snap := h.statsProvider()

	data := struct {
		StatsData
		Layout
	}{
		StatsData: snap,
		Layout:    h.layout(),
	}

	w.Header().Set("Cache-Control", "no-store")
	if err := render(w, "stats", data); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-web: stats render: %v\n", err)
	}
}
