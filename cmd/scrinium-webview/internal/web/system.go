package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
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
	Layout
	Name    string
	Content string
	IsJSON  bool
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
		Layout:  h.layout(),
		Name:    name,
		Content: content,
		IsJSON:  isJSON,
	}

	w.Header().Set("Cache-Control", "no-store")
	if err := render(w, "system", data); err != nil {
		fmt.Fprintf(os.Stderr, "scrinium-web: render system artifact: %v\n", err)
	}
}
