package web

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"sort"
	"time"
)

// Templates live as plain .html files under templates/ and are composed
// at parse time: base.html is the shared chrome (head, header nav,
// footer) styled by Pico, and each page file supplies the {{define
// "title"}} / {{define "content"}} blocks the base pulls in. The
// stylesheets live under static/ (pico.min.css plus a thin app.css) and
// are served at "<prefix>/_static/..." — see serveStatic.

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticEmbed embed.FS

// staticFS is the static/ subtree, so a request for "_static/app.css"
// resolves to "app.css" within it.
var staticFS = mustSub(staticEmbed, "static")

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("web: embed static subtree: " + err.Error())
	}
	return sub
}

// tmplFuncs are available to every page template. sortedKeys renders Go
// maps (ByStore, ViewCounts) deterministically across reloads, since map
// iteration order is randomised.
var tmplFuncs = template.FuncMap{
	"sortedKeys": func(m map[string]int64) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	},
}

// pages maps each page name to its template: the shared base layout
// composed with that page's blocks. Parsed once at init; rendering goes
// through ExecuteTemplate(w, "base", data) so every page wears the chrome.
var pages = map[string]*template.Template{
	"listing":  parsePage("listing"),
	"artifact": parsePage("artifact"),
	"stats":    parsePage("stats"),
	"search":   parsePage("search"),
	"system":   parsePage("system"),
}

func parsePage(name string) *template.Template {
	t := template.New(name).Funcs(tmplFuncs)
	return template.Must(t.ParseFS(templateFS,
		"templates/base.html", "templates/"+name+".html"))
}

// Layout holds the chrome fields every page shares — the header store
// path / nav roots / mount prefix and the footer timestamp. Page data
// structs embed it so base.html can render the header and footer
// uniformly; the per-page payload sits alongside.
type Layout struct {
	StorePath    string
	Roots        []string
	BrowsePrefix string
	NowFormatted string
}

// layout builds the shared chrome snapshot for the current request.
func (h *Handler) layout() Layout {
	return Layout{
		StorePath:    h.cfg.StorePath,
		Roots:        h.cfg.Roots,
		BrowsePrefix: h.prefix,
		NowFormatted: time.Now().UTC().Format(time.RFC3339),
	}
}

// render writes the named page to w through the shared base layout. It
// renders into a buffer first so a template error never yields a
// half-written response.
func render(w http.ResponseWriter, name string, data any) error {
	t, ok := pages[name]
	if !ok {
		return fmt.Errorf("web: unknown page %q", name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "base", data); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := buf.WriteTo(w)
	return err
}

// serveStatic serves an embedded asset (the stylesheets) requested under
// "_static/". The content type is inferred from the extension; the
// embedded FS supplies a modtime so conditional requests work.
func serveStatic(w http.ResponseWriter, r *http.Request, asset string) {
	if asset == "" {
		http.NotFound(w, r)
		return
	}
	http.ServeFileFS(w, r, staticFS, asset)
}
