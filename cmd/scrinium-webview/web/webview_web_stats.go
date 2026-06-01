package web

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"sort"
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
	Daemon     StatsDaemon
	View       StatsView
	Storage    StatsStorage
	HasStorage bool
	Extensions []StatsExtension
	Config     StatsConfig
	HasConfig  bool
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

// StatsView mirrors projection.ViewStats. ByStore is rendered
// as a sorted list inside the template; the daemon hands the
// map verbatim.
type StatsView struct {
	TotalNodes     int64
	TotalBytes     int64
	TotalBytesText string // pre-formatted "39359055 (37.5 MiB)"
	SessionCount   int64
	NamespaceCount int64
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
	Name          string
	SchemaVersion int
}

// StatsConfig mirrors the daemon's policy switches. Boolean
// fields use Go's native rendering ({{.X}} → "true"/"false");
// the template hides the section entirely via HasConfig.
type StatsConfig struct {
	ReadOnly  bool
	Editing   string // empty hides the row
	Namespace string // empty hides the row
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
		StorePath    string
		NowFormatted string
		BrowsePrefix string
		StatsURL     string
	}{
		StatsData:    snap,
		StorePath:    h.cfg.StorePath,
		NowFormatted: time.Now().UTC().Format(time.RFC3339),
		BrowsePrefix: h.prefix,
		StatsURL:     "/" + h.cfg.ServicePrefix + "/stats",
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := statsTemplate.Execute(w, data); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-web: stats template: %v\n", err)
	}
}

// statsTemplate renders the per-snapshot HTML page. Same brand
// and footer treatment as listing/artifact templates; the body
// is a series of sections matching the textual stats layout.
//
// Funcs: sortedKeys is used to render ByStore deterministically
// across reloads (Go map iteration order is randomised).
var statsTemplate = template.Must(template.New("stats").Funcs(template.FuncMap{
	"sortedKeys": func(m map[string]int64) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	},
}).Parse(statsPageHTML))

const statsPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>stats — Scrinium</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
         background: #fafafa; color: #222; margin: 0; padding: 1.5em 2em; }
  header { display: flex; align-items: baseline; gap: 1em;
           border-bottom: 1px solid #e0e0e0; padding-bottom: 0.7em; margin-bottom: 1em; }
  header .brand { font-weight: 600; color: #06f; font-size: 1.1em; letter-spacing: 0.02em; }
  header .store { color: #888; font-size: 0.9em;
                  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
  header .back  { font-size: 0.9em; }
  header .back a { color: #06f; text-decoration: none; }
  header .back a:hover { text-decoration: underline; }
  header .header-search { margin-left: auto; }
  header .header-search input { padding: 0.3em 0.6em; font-size: 0.9em;
                                  border: 1px solid #ddd; border-radius: 4px;
                                  font-family: inherit; min-width: 220px; }
  header .header-search input:focus { outline: none; border-color: #06f;
                                        box-shadow: 0 0 0 2px rgba(0,102,255,0.15); }
  h2 { font-size: 0.9em; font-weight: 500; color: #888; margin: 1.8em 0 0.6em;
       text-transform: uppercase; letter-spacing: 0.06em; }
  table { border-collapse: collapse; width: 100%; max-width: 1100px;
          margin-bottom: 1.5em; }
  td { padding: 0.4em 1em; vertical-align: top; }
  td.label { color: #888; font-size: 0.92em; width: 14em;
             font-weight: 500; }
  td.value { color: #222; font-variant-numeric: tabular-nums; }
  td.value.mono { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
                  font-size: 0.92em; word-break: break-all; }
  tr:nth-child(even) td { background: #f3f3f3; }
  footer { margin-top: 3em; padding-top: 0.8em; border-top: 1px solid #e0e0e0;
           color: #888; font-size: 0.85em; }
  footer a { color: #06f; text-decoration: none; }
  footer a:hover { text-decoration: underline; }
</style>
</head>
<body>

<header>
  <span class="brand">Scrinium</span>
  <span class="store">{{.StorePath}}</span>
  <form class="header-search" method="get" action="{{.BrowsePrefix}}/_search">
    <input type="text" name="q" placeholder="search…">
  </form>
  <span class="back"><a href="{{.BrowsePrefix}}/">← browse</a></span>
</header>

<h2>Daemon</h2>
<table>
  <tbody>
    <tr><td class="label">Source</td><td class="value">{{.Daemon.Source}}</td></tr>
    {{if not .Daemon.StartedAt.IsZero}}
    <tr><td class="label">Started</td><td class="value mono">{{.Daemon.StartedAt.UTC.Format "2006-01-02T15:04:05Z"}}</td></tr>
    <tr><td class="label">Uptime</td><td class="value">{{.Daemon.Uptime}}</td></tr>
    {{end}}
    {{if .Daemon.MountSession}}
    <tr><td class="label">MountSession</td><td class="value mono">{{.Daemon.MountSession}}</td></tr>
    {{end}}
    {{if .Daemon.StorePath}}
    <tr><td class="label">StorePath</td><td class="value mono">{{.Daemon.StorePath}}</td></tr>
    {{end}}
  </tbody>
</table>

<h2>View</h2>
<table>
  <tbody>
    <tr><td class="label">TotalNodes</td><td class="value">{{.View.TotalNodes}}</td></tr>
    <tr><td class="label">TotalBytes</td><td class="value">{{.View.TotalBytesText}}</td></tr>
    <tr><td class="label">SessionCount</td><td class="value">{{.View.SessionCount}}</td></tr>
    <tr><td class="label">NamespaceCount</td><td class="value">{{.View.NamespaceCount}}</td></tr>
    <tr><td class="label">OrphanedCount</td><td class="value">{{.View.OrphanedCount}}</td></tr>
    <tr><td class="label">CollisionCount</td><td class="value">{{.View.CollisionCount}}</td></tr>
    <tr><td class="label">TransitCount</td><td class="value">{{.View.TransitCount}}</td></tr>
    {{range $k := sortedKeys .View.ByStore}}
    <tr><td class="label">ByStore[{{$k}}]</td><td class="value">{{index $.View.ByStore $k}}</td></tr>
    {{end}}
  </tbody>
</table>

{{if .HasStorage}}
<h2>Storage</h2>
<table>
  <tbody>
    <tr><td class="label">ArtifactCount</td><td class="value">{{.Storage.ArtifactCount}}</td></tr>
    <tr><td class="label">BlobCount</td><td class="value">{{.Storage.BlobCount}}</td></tr>
    {{if .Storage.DedupRatio}}
    <tr><td class="label">DedupRatio</td><td class="value">{{.Storage.DedupRatio}}</td></tr>
    {{end}}
    <tr><td class="label">TotalBytes</td><td class="value">{{.Storage.TotalBytes}}</td></tr>
    <tr><td class="label">UsedBytes</td><td class="value">{{.Storage.UsedBytes}}</td></tr>
    <tr><td class="label">AvailableBytes</td><td class="value">{{.Storage.AvailableBytes}}</td></tr>
  </tbody>
</table>
{{end}}

{{if .Extensions}}
<h2>Extensions</h2>
<table>
  <tbody>
    {{range .Extensions}}
    <tr><td class="label mono">{{.Name}}</td><td class="value">v{{.SchemaVersion}}</td></tr>
    {{end}}
  </tbody>
</table>
{{end}}

{{if .HasConfig}}
<h2>Config</h2>
<table>
  <tbody>
    <tr><td class="label">ReadOnly</td><td class="value">{{.Config.ReadOnly}}</td></tr>
    {{if .Config.Editing}}
    <tr><td class="label">Editing</td><td class="value">{{.Config.Editing}}</td></tr>
    {{end}}
    {{if .Config.Namespace}}
    <tr><td class="label">Namespace</td><td class="value">{{.Config.Namespace}}</td></tr>
    {{end}}
  </tbody>
</table>
{{end}}

<footer>
  {{.NowFormatted}} · <a href="{{.BrowsePrefix}}/">browse</a>
</footer>

</body>
</html>
`
