package web

import (
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"
)

// searchPageData binds the search HTML template.
type searchPageData struct {
	StorePath    string
	NowFormatted string
	BrowsePrefix string
	StatsURL     string

	// Query echoes the user's input back into the form.
	Query string

	// Results is the list of hits, capped at searchLimit. Empty
	// when the query yielded nothing (or before any query was
	// submitted — that case the template differentiates via
	// HasQuery).
	Results []searchResultRow

	// HasQuery distinguishes "you haven't searched yet" (show
	// help text) from "you searched and got zero hits" (show
	// no-results message). Both produce empty Results.
	HasQuery bool

	// Truncated signals that more hits exist beyond the cap;
	// the template surfaces a note prompting the user to refine.
	Truncated bool
	Limit     int
}

// searchResultRow is one row, with display-ready strings.
type searchResultRow struct {
	URL         string
	Path        string // empty → "(orphaned)"
	Namespace   string
	SessionID   string
	CreatedAt   string
	MIME        string
	MatchReason string
	IsOrphan    bool
}

// searchLimit caps how many results we return per query.
// Higher than typical screens hold so the user sees enough
// context, low enough to keep the page snappy on big stores.
// The "Truncated" flag tells the user when they've hit the cap.
const searchLimit = 200

// serveSearch renders the search page. Empty ?q= shows just
// the form with help text; non-empty triggers the search and
// renders results.
func (h *Handler) serveSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))

	data := searchPageData{
		StorePath:    h.cfg.StorePath,
		NowFormatted: time.Now().UTC().Format(time.RFC3339),
		BrowsePrefix: h.prefix,
		StatsURL:     "/" + h.cfg.ServicePrefix + "/stats",
		Query:        q,
		HasQuery:     q != "",
		Limit:        searchLimit,
	}

	if q != "" {
		results, err := h.fs.Search(r.Context(), q, searchLimit+1)
		if err == nil {
			// Detect truncation: ask for one more than the cap;
			// if we got that many, we know more exist.
			if len(results) > searchLimit {
				data.Truncated = true
				results = results[:searchLimit]
			}
			for _, sr := range results {
				row := searchResultRow{
					URL:         h.prefix + "/_artifact/" + string(sr.ArtifactID),
					Path:        sr.Path,
					Namespace:   sr.Namespace,
					SessionID:   sr.SessionID,
					CreatedAt:   sr.CreatedAt.UTC().Format(time.RFC3339),
					MIME:        sr.MIME,
					MatchReason: sr.MatchReason,
					IsOrphan:    sr.Path == "",
				}
				data.Results = append(data.Results, row)
			}
		} else {
			fmt.Fprintf(os.Stderr, "scrinium-web: search %q: %v\n", q, err)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := searchTemplate.Execute(w, data); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-web: search template: %v\n", err)
	}
}

// searchTemplate renders the search page. Same brand and
// footer as the other pages, with the search input as the
// page's primary affordance and a results table below.
var searchTemplate = template.Must(template.New("search").Parse(searchPageHTML))

const searchPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>search — Scrinium</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
         background: #fafafa; color: #222; margin: 0; padding: 1.5em 2em; }
  header { display: flex; align-items: baseline; gap: 1em;
           border-bottom: 1px solid #e0e0e0; padding-bottom: 0.7em; margin-bottom: 1em; }
  header .brand { font-weight: 600; color: #06f; font-size: 1.1em; letter-spacing: 0.02em; }
  header .store { color: #888; font-size: 0.9em;
                  font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace; }
  header .back  { margin-left: auto; font-size: 0.9em; }
  header .back a { color: #06f; text-decoration: none; }
  header .back a:hover { text-decoration: underline; }
  form.search { margin: 1em 0 1.5em; }
  form.search input[type=text] { width: 100%; max-width: 600px; padding: 0.6em 0.8em;
                                  font-size: 1em; border: 1px solid #ccc; border-radius: 4px;
                                  font-family: inherit; }
  form.search input[type=text]:focus { outline: none; border-color: #06f;
                                         box-shadow: 0 0 0 2px rgba(0,102,255,0.15); }
  .help { color: #888; font-size: 0.9em; max-width: 600px; line-height: 1.5; }
  .help code { background: #f0f0f0; padding: 0 0.3em; border-radius: 3px;
               font-family: ui-monospace, monospace; font-size: 0.85em; }
  .truncated { background: #fff8e1; border-left: 3px solid #fb0;
               padding: 0.6em 1em; margin: 1em 0; font-size: 0.9em; color: #854; }
  .empty { color: #888; margin: 2em 0; font-style: italic; }
  table { border-collapse: collapse; width: 100%; max-width: 1100px;
          table-layout: fixed; }
  th, td { padding: 0.4em 1em; text-align: left; }
  th { font-weight: 500; color: #888; font-size: 0.9em;
       border-bottom: 1px solid #ddd; }
  th.match { width: 5em; }
  th.ns    { width: 9em; }
  th.time  { width: 11em; }
  tr:nth-child(even) td { background: #f3f3f3; }
  td.path { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
            font-size: 0.92em;
            overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
            max-width: 0; }
  td.path a { color: #06f; text-decoration: none; }
  td.path a:hover { text-decoration: underline; }
  td.path .orphan { color: #aaa; font-style: italic; }
  td.match { font-size: 0.78em; }
  td.match span { display: inline-block; padding: 0.1em 0.4em;
                  border-radius: 3px; background: #ececec; color: #888;
                  text-transform: lowercase; letter-spacing: 0.03em; }
  td.match span.path-match { background: #e0f0ff; color: #06f; }
  td.match span.id-match   { background: #e8f5e8; color: #284; }
  td.ns    { color: #666; font-size: 0.9em; }
  td.time  { color: #666; font-variant-numeric: tabular-nums;
             font-family: ui-monospace, monospace; font-size: 0.85em;
             white-space: nowrap; }
  .summary { margin-top: 0.6em; color: #888; font-size: 0.85em; }
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
  <span class="back"><a href="{{.BrowsePrefix}}/">← back to browse</a></span>
</header>

<form class="search" method="get" action="{{.BrowsePrefix}}/_search">
  <input type="text" name="q" value="{{.Query}}" placeholder="search by path, namespace, or artifact id…" autofocus>
</form>

{{if not .HasQuery}}
<div class="help">
  Enter a substring to find matching artifacts. Searches:
  <ul>
    <li>artifact paths (case-insensitive substring) — e.g. <code>sunset</code> finds <code>/photos/2024/sunset.jpg</code>;</li>
    <li>namespaces (case-insensitive substring) — e.g. <code>mail</code> finds <code>mail-archive</code>;</li>
    <li>artifact ids (exact match) — paste a full id to jump straight to it.</li>
  </ul>
</div>
{{else if not .Results}}
<p class="empty">No artifacts match <strong>{{.Query}}</strong>.</p>
{{else}}
{{if .Truncated}}
<div class="truncated">
  Showing the first {{.Limit}} matches. Refine the query to see more.
</div>
{{end}}
<table>
  <thead>
    <tr>
      <th>Path</th>
      <th class="match">Match</th>
      <th class="ns">Namespace</th>
      <th class="time">Created</th>
    </tr>
  </thead>
  <tbody>
{{- range .Results}}
    <tr>
      <td class="path"><a href="{{.URL}}" title="{{if .IsOrphan}}(orphaned){{else}}{{.Path}}{{end}}">{{if .IsOrphan}}<span class="orphan">(orphaned)</span>{{else}}{{.Path}}{{end}}</a></td>
      <td class="match"><span class="{{.MatchReason}}-match">{{.MatchReason}}</span></td>
      <td class="ns">{{if .Namespace}}{{.Namespace}}{{else}}—{{end}}</td>
      <td class="time">{{.CreatedAt}}</td>
    </tr>
{{- end}}
  </tbody>
</table>
<div class="summary">
  {{len .Results}} {{if eq (len .Results) 1}}result{{else}}results{{end}}{{if .Truncated}} (capped){{end}}
</div>
{{end}}

<footer>
  {{.NowFormatted}} · <a href="{{.BrowsePrefix}}/_stats">stats</a> · <a href="{{.BrowsePrefix}}/">browse</a>
</footer>

</body>
</html>
`
