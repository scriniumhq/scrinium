package web

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rkurbatov/scrinium/domain"
)

// SchemaDecoder is the contract for plugging schema-aware
// rendering into the artifact details page. Daemons register
// decoders at startup via Handler.RegisterDecoder; the web pkg
// itself ships none — it stays schema-agnostic so any host
// (FUSE, WebDAV, future ones) can install whatever they care
// about.
//
// Marker is the schema's "kind" field as it appears in
// Manifest.Metadata (e.g. "scrinium.fs/v1"). Render produces an
// HTML fragment for the specific schema; the web pkg slots it
// into the artifact page's "Schema" section.
//
// Decoder errors don't break the page — the handler falls back
// to the generic JSON view and notes the error inline.
type SchemaDecoder interface {
	Marker() string
	Render(raw json.RawMessage) (template.HTML, error)
}

// RegisterDecoder installs a schema decoder. Subsequent calls
// with the same Marker overwrite the previous registration —
// the daemon sets up its decoders at boot, in a fixed order;
// later, last-write-wins is the simplest contract.
//
// Concurrent calls during request handling are not supported;
// register decoders before mounting the handler.
func (h *Handler) RegisterDecoder(d SchemaDecoder) {
	if h.decoders == nil {
		h.decoders = map[string]SchemaDecoder{}
	}
	h.decoders[d.Marker()] = d
}

// schemaPeek is the minimal shape we read from
// Manifest.Metadata to dispatch on schema kind. Both
// scrinium.fs/v1 and any future schema must include a "kind"
// field; decoders without one fall through to the JSON view.
type schemaPeek struct {
	Kind string `json:"kind"`
}

// serveArtifact renders the details page for one artifact.
func (h *Handler) serveArtifact(w http.ResponseWriter, r *http.Request, id domain.ArtifactID) {
	m, ok, err := h.fs.LookupManifest(r.Context(), id)
	if err != nil {
		h.serveError(w, http.StatusInternalServerError,
			fmt.Sprintf("lookup %q: %v", id, err))
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	data, renderErr := h.buildArtifactData(m)
	if renderErr != nil {
		// The page still renders even when individual
		// sub-renders fail — the user sees the unaffected
		// sections plus the error inline. This mirrors
		// "best effort" rendering elsewhere in the daemon.
		fmt.Fprintf(os.Stderr, "scrinium-web: artifact %q render: %v\n", id, renderErr)
	}

	// Hex preview is best-effort: if OpenArtifact fails
	// (encrypted-locked store, missing blob, large external
	// URI we don't traverse) we just leave the preview empty
	// and the template skips the section.
	data.HexPreview = h.tryHexPreview(r.Context(), id)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if execErr := artifactTemplate.Execute(w, data); execErr != nil {
		fmt.Fprintf(os.Stderr, "scrinium-web: artifact template: %v\n", execErr)
	}
}

// hexPreviewBytes is the cap on how many bytes of the artifact
// we include in the hex preview. 256 keeps the page small and
// shows the magic header of any common file format — enough to
// identify what's inside without making the page heavy for
// gigabyte-sized blobs.
const hexPreviewBytes = 256

// tryHexPreview opens the artifact, reads up to hexPreviewBytes,
// and renders a hexdump-style string. Best-effort: returns ""
// on any error (which the template treats as "no preview").
func (h *Handler) tryHexPreview(ctx context.Context, id domain.ArtifactID) string {
	f, _, err := h.fs.OpenArtifact(ctx, id)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, hexPreviewBytes)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return ""
	}
	if n == 0 {
		return ""
	}
	return formatHexDump(buf[:n])
}

// formatHexDump renders bytes in classic xxd / hexdump -C
// layout: "OFFSET  HEX-PAIRS  |ASCII|" with 16 bytes per row.
// Non-printable ASCII becomes ".".
func formatHexDump(data []byte) string {
	var b strings.Builder
	const cols = 16
	for off := 0; off < len(data); off += cols {
		end := off + cols
		if end > len(data) {
			end = len(data)
		}
		row := data[off:end]

		fmt.Fprintf(&b, "%08x  ", off)

		// Hex pairs, with a centre gap after 8 bytes (matching
		// hexdump -C aesthetic).
		for i := 0; i < cols; i++ {
			if i == 8 {
				b.WriteByte(' ')
			}
			if i < len(row) {
				fmt.Fprintf(&b, "%02x ", row[i])
			} else {
				b.WriteString("   ")
			}
		}

		b.WriteString(" |")
		for _, c := range row {
			if c >= 0x20 && c < 0x7f {
				b.WriteByte(c)
			} else {
				b.WriteByte('.')
			}
		}
		b.WriteString("|\n")
	}
	return b.String()
}

// artifactPageData is what artifactTemplate consumes.
type artifactPageData struct {
	StorePath    string
	NowFormatted string
	StatsURL     string
	BrowsePrefix string

	// Identity & storage are flat tables of label/value rows.
	// We render them as ordered slices instead of maps so the
	// row order is deterministic across reloads.
	Identity []labelValue
	Storage  []labelValue

	// Pipeline is the per-stage transform list. Empty for
	// artifacts that went straight to disk untransformed.
	Pipeline []pipelineStageView

	// Schema renders one of:
	//   - SchemaHTML — when a registered decoder claimed the
	//     metadata's "kind". Trusted HTML, the decoder owns it.
	//   - SchemaJSON — pretty-printed fallback when no decoder
	//     applied or the decoder errored.
	//   - SchemaError — note shown above the JSON when a
	//     decoder explicitly returned an error.
	SchemaKind  string
	SchemaHTML  template.HTML
	SchemaJSON  string
	SchemaError string

	// RawJSON is the full manifest in indented JSON, shown
	// inside a <details> at the bottom of the page.
	RawJSON string

	// HexPreview is a hexdump-style view of the first ~256
	// bytes of the artifact's payload. Presented inside a
	// <details> so the user opts into reading bytes; for
	// large artifacts we never fetch more than the preview
	// window. Empty when the artifact's bytes couldn't be
	// fetched (encrypted-locked store, missing blob, etc.).
	HexPreview string
}

// labelValue is one row of a label/value table.
type labelValue struct {
	Label string
	Value string
	// Mono toggles monospace rendering for hash-shaped values.
	Mono bool
}

// pipelineStageView is one row of the pipeline table.
type pipelineStageView struct {
	Index     int
	Algorithm string
	Hash      string
	IVHex     string
}

// buildArtifactData fills the template payload. Errors are
// returned but only as diagnostics — the data is always
// renderable, errors signal partial degradation.
func (h *Handler) buildArtifactData(m domain.Manifest) (artifactPageData, error) {
	data := artifactPageData{
		StorePath:    h.cfg.StorePath,
		NowFormatted: time.Now().UTC().Format(time.RFC3339),
		StatsURL:     "/" + h.cfg.ServicePrefix + "/stats",
		BrowsePrefix: h.prefix,
	}

	data.Identity = []labelValue{
		{Label: "ArtifactID", Value: string(m.ArtifactID), Mono: true},
		{Label: "Type", Value: string(m.Type)},
		{Label: "Namespace", Value: orDash(m.Namespace)},
		{Label: "SessionID", Value: orDash(m.SessionID), Mono: m.SessionID != ""},
		{Label: "CreatedAt", Value: m.CreatedAt.UTC().Format(time.RFC3339)},
		{Label: "RetentionUntil", Value: formatTimeOrDash(m.RetentionUntil)},
	}

	data.Storage = []labelValue{
		{Label: "BlobRef", Value: string(m.BlobRef), Mono: true},
		{Label: "ContentHash", Value: string(m.ContentHash), Mono: true},
		{Label: "OriginalSize", Value: fmt.Sprintf("%d (%s)", m.OriginalSize, HumanSize(m.OriginalSize))},
		{Label: "Layout", Value: orDash(m.LayoutHeader.BlobStorage)},
		{Label: "KeyID", Value: orDash(m.KeyID), Mono: m.KeyID != ""},
	}
	if m.ExternalURI != "" {
		data.Storage = append(data.Storage, labelValue{
			Label: "ExternalURI", Value: m.ExternalURI, Mono: true,
		})
	}
	if len(m.InlineBlob) > 0 {
		data.Storage = append(data.Storage, labelValue{
			Label: "InlineBlob",
			Value: fmt.Sprintf("%d bytes", len(m.InlineBlob)),
		})
	}

	for i, stage := range m.Pipeline {
		data.Pipeline = append(data.Pipeline, pipelineStageView{
			Index:     i,
			Algorithm: stage.Algorithm,
			Hash:      stage.Hash,
			IVHex:     hex.EncodeToString(stage.IV),
		})
	}

	// Schema rendering. Three branches:
	//
	//   1. Metadata is empty → no Schema section. Template
	//      renders nothing.
	//   2. Metadata has a "kind" matching a registered decoder
	//      → render through the decoder; on error, fall back
	//      to JSON with the error noted.
	//   3. Otherwise → pretty JSON view, no kind highlighted.
	if len(m.Metadata) > 0 {
		var peek schemaPeek
		_ = json.Unmarshal(m.Metadata, &peek) // best-effort
		data.SchemaKind = peek.Kind
		if dec, ok := h.decoders[peek.Kind]; ok && peek.Kind != "" {
			rendered, err := dec.Render(m.Metadata)
			if err != nil {
				data.SchemaError = err.Error()
				data.SchemaJSON = prettyJSON(m.Metadata)
			} else {
				data.SchemaHTML = rendered
			}
		} else {
			data.SchemaJSON = prettyJSON(m.Metadata)
		}
	}

	// Raw manifest JSON. We construct a small struct mirroring
	// the wire shape (ArtifactID is intentionally absent — it's
	// derived, not serialised, per docs §7.4).
	raw, err := json.MarshalIndent(struct {
		Type           domain.ManifestType    `json:"type"`
		Namespace      string                 `json:"namespace,omitempty"`
		SessionID      string                 `json:"session_id,omitempty"`
		CreatedAt      time.Time              `json:"created_at"`
		ContentHash    domain.ContentHash     `json:"content_hash,omitempty"`
		OriginalSize   int64                  `json:"original_size"`
		BlobRef        domain.BlobRef         `json:"blob_ref,omitempty"`
		LayoutHeader   domain.LayoutHeader    `json:"layout_header"`
		Pipeline       []domain.PipelineStage `json:"pipeline,omitempty"`
		ExternalURI    string                 `json:"external_uri,omitempty"`
		RetentionUntil time.Time              `json:"retention_until,omitempty"`
		KeyID          string                 `json:"key_id,omitempty"`
		Metadata       json.RawMessage        `json:"metadata,omitempty"`
	}{
		Type:           m.Type,
		Namespace:      m.Namespace,
		SessionID:      m.SessionID,
		CreatedAt:      m.CreatedAt,
		ContentHash:    m.ContentHash,
		OriginalSize:   m.OriginalSize,
		BlobRef:        m.BlobRef,
		LayoutHeader:   m.LayoutHeader,
		Pipeline:       m.Pipeline,
		ExternalURI:    m.ExternalURI,
		RetentionUntil: m.RetentionUntil,
		KeyID:          m.KeyID,
		Metadata:       m.Metadata,
	}, "", "  ")
	if err == nil {
		data.RawJSON = string(raw)
	} else {
		return data, fmt.Errorf("marshal manifest: %w", err)
	}

	return data, nil
}

// prettyJSON re-formats a json.RawMessage with two-space
// indent. On parse failure we return the original bytes — the
// page still shows them, the user sees they're not pretty.
func prettyJSON(raw json.RawMessage) string {
	var any interface{}
	if err := json.Unmarshal(raw, &any); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(any, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// orDash returns s, or "—" when s is empty. Used for table
// rows where blank values would look awkward.
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// formatTimeOrDash renders an RFC3339 timestamp, or "—" for
// the zero time (the convention RetentionUntil uses for
// "never expires").
func formatTimeOrDash(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.UTC().Format(time.RFC3339)
}

// --- listing integration ---
//
// The listing page also needs to know the artifact id of each
// file row so it can render an "info" link. We extend the
// existing dirEntry → listingRow pipeline below; the FileInfo
// we get from BackingFS doesn't carry the id, so the host has
// to provide it via a side channel. The simplest is letting
// FileInfo implementations expose ArtifactID via type-assertion.

// ArtifactInfo is the optional interface a FileInfo can
// implement to surface its underlying ArtifactID. The web
// listing handler probes for it; if absent, the row gets no
// info link (typical for virtual directories).
type ArtifactInfo interface {
	ArtifactID() domain.ArtifactID
}

// MIMEInfo is the optional interface a FileInfo can implement
// to surface its content's MIME type. Probed by the listing
// handler to decide whether to advertise a [view] button. If
// absent or returns empty, view is not offered — we never
// advertise inline rendering for unknown types.
type MIMEInfo interface {
	MIME() string
}

// extractArtifactID reads the id from a FileInfo if available.
// Empty string when the FileInfo doesn't expose one.
func extractArtifactID(fi os.FileInfo) domain.ArtifactID {
	if a, ok := fi.(ArtifactInfo); ok {
		return a.ArtifactID()
	}
	return ""
}

// extractMIME reads the MIME from a FileInfo if available.
// Empty string when no source could provide one.
func extractMIME(fi os.FileInfo) string {
	if m, ok := fi.(MIMEInfo); ok {
		return m.MIME()
	}
	return ""
}

// --- artifact template ---

// artifactTemplate renders the per-artifact details page.
// Same brand and footer aesthetic as the listing template,
// but the body is structured around the four sections
// (Identity, Storage, Pipeline, Schema, Raw).
var artifactTemplate = template.Must(template.New("artifact").Parse(artifactPageHTML))

const artifactPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>artifact — Scrinium</title>
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
  h2   { font-size: 0.9em; font-weight: 500; color: #888; margin: 1.8em 0 0.6em;
         text-transform: uppercase; letter-spacing: 0.06em; }
  table { border-collapse: collapse; width: 100%; max-width: 1100px;
          margin-bottom: 1.5em; }
  th { display: none; }
  td { padding: 0.4em 1em; vertical-align: top; }
  td.label { color: #888; font-size: 0.92em; width: 12em;
             font-weight: 500; }
  td.value { color: #222; }
  td.value.mono { font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
                  font-size: 0.92em; word-break: break-all; }
  tr:nth-child(even) td { background: #f3f3f3; }
  table.pipeline td.idx { width: 3em; color: #888; font-variant-numeric: tabular-nums; }
  table.pipeline td.algo { font-weight: 500; }
  table.pipeline td.hash, table.pipeline td.iv { font-family: ui-monospace, monospace;
                                                  font-size: 0.85em; color: #555;
                                                  word-break: break-all; }
  .schema-kind { display: inline-block; padding: 0.1em 0.5em; background: #06f;
                 color: white; border-radius: 3px; font-size: 0.78em;
                 letter-spacing: 0.04em; vertical-align: 0.1em; margin-left: 0.5em; }
  .schema-error { background: #fdf3f3; color: #b22; padding: 0.6em 1em;
                  border-left: 3px solid #c33; margin-bottom: 1em;
                  font-size: 0.9em; }
  pre { background: #f0f0f0; padding: 1em; overflow-x: auto;
        font-family: ui-monospace, "SF Mono", Menlo, Consolas, monospace;
        font-size: 0.85em; line-height: 1.5; border-radius: 4px;
        border: 1px solid #e0e0e0; }
  pre.hexdump { font-size: 0.78em; line-height: 1.4; letter-spacing: 0.02em; }
  details summary { cursor: pointer; color: #888; font-size: 0.9em;
                    margin: 1.5em 0 0.5em; }
  details summary:hover { color: #06f; }
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

<h2>Identity</h2>
<table>
  <tbody>
{{- range .Identity}}
    <tr>
      <td class="label">{{.Label}}</td>
      <td class="value{{if .Mono}} mono{{end}}">{{.Value}}</td>
    </tr>
{{- end}}
  </tbody>
</table>

<h2>Storage</h2>
<table>
  <tbody>
{{- range .Storage}}
    <tr>
      <td class="label">{{.Label}}</td>
      <td class="value{{if .Mono}} mono{{end}}">{{.Value}}</td>
    </tr>
{{- end}}
  </tbody>
</table>

{{if .Pipeline}}
<h2>Pipeline</h2>
<table class="pipeline">
  <tbody>
{{- range .Pipeline}}
    <tr>
      <td class="idx">#{{.Index}}</td>
      <td class="algo">{{.Algorithm}}</td>
      <td class="hash">{{.Hash}}</td>
      <td class="iv">{{if .IVHex}}IV: {{.IVHex}}{{end}}</td>
    </tr>
{{- end}}
  </tbody>
</table>
{{end}}

{{if or .SchemaHTML .SchemaJSON}}
<h2>Schema {{if .SchemaKind}}<span class="schema-kind">{{.SchemaKind}}</span>{{end}}</h2>
{{if .SchemaError}}<div class="schema-error">decoder error: {{.SchemaError}}</div>{{end}}
{{if .SchemaHTML}}{{.SchemaHTML}}{{else}}<pre>{{.SchemaJSON}}</pre>{{end}}
{{end}}

{{if .HexPreview}}
<details>
  <summary>Hex preview (first 256 bytes)</summary>
  <pre class="hexdump">{{.HexPreview}}</pre>
</details>
{{end}}

<details>
  <summary>Raw manifest JSON</summary>
  <pre>{{.RawJSON}}</pre>
</details>

<footer>
  {{.NowFormatted}} · <a href="{{.StatsURL}}">stats</a> · <a href="{{.BrowsePrefix}}/">browse</a>
</footer>

</body>
</html>
`
