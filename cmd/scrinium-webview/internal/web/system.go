package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"strings"
	"time"
)

// matchSystemRoute parses "_system/<name>" at the top of the
// browser-relative path. System artifact names are dot-separated and never
// contain a slash (the planar keyspace, ADR-85), so a name with a slash is
// rejected rather than treated as a sub-path.
func matchSystemRoute(rel string) (string, bool) {
	const prefix = "_system/"
	if !strings.HasPrefix(rel, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(rel, prefix)
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

// systemArtifactData backs the system-artifact viewer page.
type systemArtifactData struct {
	Name         string
	Content      string
	IsJSON       bool
	StorePath    string
	BrowsePrefix string
	StatsURL     string
	NowFormatted string
}

// serveSystemArtifact renders the active version of one system artifact.
// The payload is shown verbatim; when it parses as JSON (the common case —
// most envelopes are JSON: config, the namespace registry, agent cursors,
// leases) it is pretty-printed. A pointer artifact's external blob streams
// through too, capped by the BackingFS.
func (h *Handler) serveSystemArtifact(w http.ResponseWriter, r *http.Request, name string) {
	body, ok, err := h.fs.SystemArtifact(r.Context(), name)
	if err != nil {
		h.serveError(w, http.StatusInternalServerError, "read system artifact: "+err.Error())
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	content := string(body)
	isJSON := false
	var buf bytes.Buffer
	if json.Indent(&buf, body, "", "  ") == nil {
		content = buf.String()
		isJSON = true
	}

	data := systemArtifactData{
		Name:         name,
		Content:      content,
		IsJSON:       isJSON,
		StorePath:    h.cfg.StorePath,
		BrowsePrefix: h.prefix,
		StatsURL:     "/" + h.cfg.ServicePrefix + "/stats",
		NowFormatted: time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := systemArtifactTemplate.Execute(w, data); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-web: render system artifact: %v\n", err)
	}
}

var systemArtifactTemplate = template.Must(template.New("system").Parse(systemArtifactPageHTML))

const systemArtifactPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Name}} · system artifact</title>
<style>
  body { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; margin: 0; color: #1a1a1a; }
  header { display: flex; align-items: baseline; gap: 1rem; padding: .75rem 1rem; border-bottom: 1px solid #ddd; }
  .brand { font-weight: 700; }
  .store { color: #666; font-size: .85rem; }
  main { padding: 1rem; }
  h1 { font-size: 1rem; word-break: break-all; }
  .meta { color: #666; font-size: .85rem; margin-bottom: .75rem; }
  pre { background: #f6f6f6; border: 1px solid #e2e2e2; border-radius: 4px; padding: 1rem; overflow-x: auto; white-space: pre-wrap; word-break: break-word; }
  footer { padding: 1rem; color: #666; font-size: .85rem; border-top: 1px solid #ddd; }
  a { color: #0a58ca; }
</style>
</head>
<body>
<header>
  <span class="brand">Scrinium</span>
  <span class="store">{{.StorePath}}</span>
</header>
<main>
  <h1>{{.Name}}</h1>
  {{if .Content}}
  <p class="meta">{{if .IsJSON}}JSON{{else}}raw payload{{end}}</p>
  <pre>{{.Content}}</pre>
  {{else}}
  <p class="meta">empty payload</p>
  {{end}}
</main>
<footer>
  {{.NowFormatted}} · <a href="{{.StatsURL}}">stats</a> · <a href="{{.BrowsePrefix}}/">browse</a>
</footer>
</body>
</html>
`
