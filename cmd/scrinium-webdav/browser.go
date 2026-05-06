package main

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// browserHandler renders HTML directory listings for the
// secondary, human-facing surface mounted at cfg.BrowsePrefix
// (default "/_browse"). WebDAV stays on the root path
// untouched — this is a separate handler in the same mux,
// not a middleware around WebDAV.
//
// Why a separate path: WebDAV clients (Finder, rclone, Office)
// expect strict protocol behaviour at the root. Returning HTML
// for GET on a directory works for browsers but risks
// confusing some older WebDAV clients that issue GET on
// directories before falling back to PROPFIND. Keeping the
// browser under its own prefix means there's exactly one
// canonical surface per consumer: WebDAV at "/", browser at
// "/_browse/", they never overlap.
type browserHandler struct {
	wfs    *webdavFS
	cfg    Config
	prefix string // normalised: leading "/" present, trailing "/" stripped
}

func newBrowserHandler(wfs *webdavFS, cfg Config) *browserHandler {
	prefix := strings.TrimRight(cfg.BrowsePrefix, "/")
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return &browserHandler{wfs: wfs, cfg: cfg, prefix: prefix}
}

func (b *browserHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Strip the configured prefix from the URL path. mux gives
	// us a path that begins with prefix; we need the
	// store-relative remainder for Stat/OpenFile lookups.
	rel := strings.TrimPrefix(r.URL.Path, b.prefix)
	rel = strings.TrimPrefix(rel, "/")
	clean := cleanWebDAVPath(rel)

	fi, err := b.wfs.Stat(r.Context(), clean)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if !fi.IsDir() {
		// Files: stream through OpenFile + http.ServeContent.
		// We don't reuse the WebDAV handler's GET path because
		// that would double-route through the prefix logic.
		b.serveFile(w, r, clean, fi)
		return
	}

	// Directory: render listing.
	entries, err := b.dirEntries(r.Context(), clean)
	if err != nil {
		http.Error(w, fmt.Sprintf("scrinium: list %q: %v", clean, err),
			http.StatusInternalServerError)
		return
	}
	b.renderListing(w, r, clean, entries)
}

// serveFile streams a non-directory through OpenFile. The
// underlying readHandleFile satisfies io.Seeker, so we use
// http.ServeContent for proper Range/Last-Modified handling.
func (b *browserHandler) serveFile(w http.ResponseWriter, r *http.Request, name string, fi os.FileInfo) {
	f, err := b.wfs.OpenFile(r.Context(), name, os.O_RDONLY, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	// f is a webdav.File which is io.ReadSeeker — exactly
	// what http.ServeContent wants. Browser sets Content-Type
	// from filename extension via http.DetectContentType.
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// dirEntry is one row in the rendered listing. Computed
// up-front so the template doesn't have to call helpers per row.
type dirEntry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
}

// dirEntries enumerates a directory through OpenFile + Readdir
// (the same code paths the WebDAV side uses, so junk filtering
// and service-tree synthesis behave identically).
func (b *browserHandler) dirEntries(ctx context.Context, dir string) ([]dirEntry, error) {
	f, err := b.wfs.OpenFile(ctx, dir, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	infos, err := f.Readdir(-1)
	if err != nil && err != io.EOF {
		return nil, err
	}
	out := make([]dirEntry, 0, len(infos))
	for _, fi := range infos {
		out = append(out, dirEntry{
			Name:    fi.Name(),
			IsDir:   fi.IsDir(),
			Size:    fi.Size(),
			ModTime: fi.ModTime(),
		})
	}
	// Stable order: directories first, then files; both
	// sorted by name. Browsers don't reorder, so we have to.
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// listingData binds the HTML template.
type listingData struct {
	Path         string
	Crumbs       []crumb
	Parent       string
	HasParent    bool
	Entries      []listingRow
	StorePath    string
	NowFormatted string
	StatsURL     string
}

type crumb struct {
	Name string
	URL  string
}

type listingRow struct {
	Name     string
	URL      string
	IsDir    bool
	SizeText string
	TimeText string
}

func (b *browserHandler) renderListing(w http.ResponseWriter, r *http.Request, dir string, entries []dirEntry) {
	data := listingData{
		Path:         "/" + dir,
		Crumbs:       b.buildCrumbs(dir),
		StorePath:    b.cfg.StorePath,
		NowFormatted: time.Now().UTC().Format(time.RFC3339),
		StatsURL:     "/" + b.cfg.ServicePrefix + "/stats",
	}
	if dir != "" {
		parent := path.Dir(dir)
		if parent == "." {
			parent = ""
		}
		if parent == "" {
			data.Parent = b.prefix + "/"
		} else {
			data.Parent = b.prefix + "/" + parent + "/"
		}
		data.HasParent = true
	}

	for _, e := range entries {
		row := listingRow{
			Name:     e.Name,
			IsDir:    e.IsDir,
			TimeText: e.ModTime.UTC().Format("2006-01-02 15:04"),
		}
		base := r.URL.Path
		if !strings.HasSuffix(base, "/") {
			base += "/"
		}
		row.URL = base + e.Name
		if e.IsDir {
			row.URL += "/"
			row.SizeText = "—"
		} else {
			row.SizeText = humanSize(e.Size)
		}
		data.Entries = append(data.Entries, row)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := listingTemplate.Execute(w, data); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-webdav: render listing: %v\n", err)
	}
}

// buildCrumbs renders the path bar above the listing. Each
// crumb is clickable; the root crumb points back at the
// browser prefix.
func (b *browserHandler) buildCrumbs(dir string) []crumb {
	out := []crumb{{Name: "root", URL: b.prefix + "/"}}
	if dir == "" {
		return out
	}
	parts := strings.Split(dir, "/")
	acc := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if acc == "" {
			acc = p
		} else {
			acc = acc + "/" + p
		}
		out = append(out, crumb{
			Name: p,
			URL:  b.prefix + "/" + acc + "/",
		})
	}
	return out
}

// humanSize renders bytes in the largest unit under 1024.
// Mirrors projection.RenderStats's helper but kept here to
// avoid pulling the projection package's helpers into cmd.
func humanSize(n int64) string {
	const (
		KiB = 1024
		MiB = 1024 * KiB
		GiB = 1024 * MiB
		TiB = 1024 * GiB
	)
	switch {
	case n >= TiB:
		return fmt.Sprintf("%.1f TiB", float64(n)/float64(TiB))
	case n >= GiB:
		return fmt.Sprintf("%.1f GiB", float64(n)/float64(GiB))
	case n >= MiB:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(MiB))
	case n >= KiB:
		return fmt.Sprintf("%.1f KiB", float64(n)/float64(KiB))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// listingTemplate is the embedded HTML template. Plain HTML +
// minimal CSS (legibility-only, no JavaScript). System fonts so
// we don't depend on a network-loaded font.
var listingTemplate = template.Must(template.New("listing").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Path}} — Scrinium</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
         background: #fafafa; color: #222; margin: 0; padding: 1.5em 2em; }
  header { display: flex; align-items: baseline; gap: 1em;
           border-bottom: 1px solid #e0e0e0; padding-bottom: 0.7em; margin-bottom: 1em; }
  header .brand { font-weight: 600; color: #06f; font-size: 1.1em; letter-spacing: 0.02em; }
  header .store { color: #888; font-size: 0.9em;
                  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
  .crumbs { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
            font-size: 0.95em; margin-bottom: 1.2em; }
  .crumbs a { color: #06f; text-decoration: none; }
  .crumbs a:hover { text-decoration: underline; }
  .crumbs .sep { color: #aaa; margin: 0 0.3em; }
  table { border-collapse: collapse; width: 100%; max-width: 1100px; }
  th, td { padding: 0.4em 1em; text-align: left; }
  th { font-weight: 500; color: #888; font-size: 0.9em;
       border-bottom: 1px solid #ddd; }
  tr:nth-child(even) td { background: #f3f3f3; }
  td.name { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
  td.name a { color: #06f; text-decoration: none; }
  td.name a:hover { text-decoration: underline; }
  td.size, td.time { color: #666; font-variant-numeric: tabular-nums;
                     font-family: ui-monospace, monospace; font-size: 0.92em; }
  td.size { text-align: right; }
  .icon-dir  { color: #999; margin-right: 0.5em; }
  .icon-file { color: #ccc; margin-right: 0.5em; }
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
</header>

<div class="crumbs">
  {{range $i, $c := .Crumbs}}{{if $i}}<span class="sep">/</span>{{end}}<a href="{{$c.URL}}">{{$c.Name}}</a>{{end}}
</div>

<table>
  <thead>
    <tr>
      <th>Name</th>
      <th class="size">Size</th>
      <th class="time">Modified</th>
    </tr>
  </thead>
  <tbody>
{{- if .HasParent}}
    <tr>
      <td class="name"><span class="icon-dir">↑</span><a href="{{.Parent}}">..</a></td>
      <td class="size">—</td>
      <td class="time"></td>
    </tr>
{{- end}}
{{- range .Entries}}
    <tr>
      <td class="name">{{if .IsDir}}<span class="icon-dir">▸</span>{{else}}<span class="icon-file">·</span>{{end}}<a href="{{.URL}}">{{.Name}}{{if .IsDir}}/{{end}}</a></td>
      <td class="size">{{.SizeText}}</td>
      <td class="time">{{.TimeText}}</td>
    </tr>
{{- end}}
  </tbody>
</table>

<footer>
  {{.NowFormatted}} · <a href="{{.StatsURL}}">stats</a>
</footer>

</body>
</html>
`))
