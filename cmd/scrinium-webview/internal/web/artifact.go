package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"scrinium.dev/cmd/internal/humanize"
	"scrinium.dev/domain"
	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/projection"
	"scrinium.dev/projection/pathx"
)

// SchemaDecoder is the contract for plugging schema-aware
// rendering into the artifact details page. Daemons register
// decoders at startup via Handler.RegisterDecoder; the web pkg
// itself ships none — it stays schema-agnostic so any host
// (FUSE, WebDAV, future ones) can install whatever they care
// about.
//
// Key is the schema's key in Manifest.Ext (e.g. "vfsmeta",
// "email", "acme.archive"). Render receives the full Ext object
// and pulls its own key from it, producing an HTML fragment for
// the schema; the web pkg slots it into the artifact page's
// "Schema" section.
//
// Decoder errors don't break the page — the handler falls back
// to the generic JSON view and notes the error inline.
type SchemaDecoder interface {
	Key() string
	Render(ext json.RawMessage) (template.HTML, error)
}

// RegisterDecoder installs a schema decoder. Subsequent calls
// with the same Key overwrite the previous registration —
// the daemon sets up its decoders at boot, in a fixed order;
// later, last-write-wins is the simplest contract.
//
// Concurrent calls during request handling are not supported;
// register decoders before mounting the handler.
func (h *Handler) RegisterDecoder(d SchemaDecoder) {
	if h.decoders == nil {
		h.decoders = map[string]SchemaDecoder{}
	}
	h.decoders[d.Key()] = d
}

// extKeys reads the top-level keys of a Manifest.Ext object so the
// handler can dispatch each schema block to its registered decoder.
// Manifest.Ext is a JSON object keyed by schema name ("vfsmeta",
// "namespace", "email", ...); a non-object or empty Ext yields no keys.
func extKeys(ext json.RawMessage) []string {
	if len(ext) == 0 {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(ext, &obj); err != nil {
		return nil
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	return keys
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

	data, renderErr := h.buildArtifactData(r.Context(), m)
	if renderErr != nil {
		// The page still renders even when individual
		// sub-renders fail — the user sees the unaffected
		// sections plus the error inline. This mirrors
		// "best effort" rendering elsewhere in the daemon.
		h.log.Error("artifact render", "id", string(id), "err", renderErr)
	}

	// Content preview: dispatched on MIME so JSON/XML/CSV/text
	// get readable rendering and everything else falls back to
	// hex. Best-effort throughout — failure leaves the
	// section hidden.
	body, kind, note, isTable := h.tryPreview(r.Context(), id, m)
	data.PreviewKind = kind
	data.PreviewNote = note
	data.PreviewIsTable = isTable
	// Open by default for previews where the content itself is
	// the point. Hex stays collapsed: it's a diagnostic dump,
	// not the natural way to look at a file.
	data.PreviewOpen = kind != "" && !strings.HasPrefix(kind, "Hex")
	if isTable {
		// Table/image content is already-formatted HTML; mark
		// it trusted for the template.
		data.PreviewHTML = template.HTML(body)
	} else {
		data.Preview = body
	}

	w.Header().Set("Cache-Control", "no-store")
	if execErr := render(w, "artifact", data); execErr != nil {
		h.log.Error("artifact render", "err", execErr)
	}
}

// hexPreviewBytes is the cap on bytes shown in the hex preview.
// 256 keeps the page small and shows the magic header of any
// common file format — enough to identify what's inside without
// making the page heavy for gigabyte-sized blobs.
const hexPreviewBytes = 256

// textPreviewBytes is the cap for content-aware previews
// (JSON / XML / CSV / plain text). Larger than hexPreviewBytes
// because structured formats need enough bytes to surface a
// useful chunk of the document; 64 KiB fits a sizable JSON
// object or hundreds of CSV rows without bloating the page.
const textPreviewBytes = 64 * 1024

// tryPreview reads the artifact's first bytes and renders them
// in a format suited to the MIME type:
//
//   - image/* (whitelist) → <img> pointing at /_view/<id>.
//   - application/json → pretty-printed with 2-space indent.
//   - application/xml or text/xml → indented XML.
//   - text/csv → HTML table (first row as header).
//   - text/plain or text/markdown → plain pre-formatted text.
//   - everything else → hex dump (the fallback).
//
// Best-effort throughout: any read or parse error falls back to
// hex preview. The artifact-page template skips the section
// entirely when the returned string is empty.
//
// Returns (body, kindLabel, note, isTable). isTable means body
// is already HTML and should be rendered verbatim; the other
// kinds are plain text the template wraps in <pre>.
func (h *Handler) tryPreview(ctx context.Context, id domain.ArtifactID, m domain.Manifest) (body, kind, note string, isTable bool) {
	mimeType := previewMIME(m)

	// Images don't need bytes — we render an <img> pointing at
	// /_view/<id> and let the browser scale it. Short-circuit
	// before opening the artifact so we don't pay the I/O for
	// a preview the browser fetches anyway through a separate
	// request.
	if isImageInlineable(mimeType) {
		html := fmt.Sprintf(`<img class="img-preview" src="%s/_view/%s" alt="">`,
			template.HTMLEscapeString(h.prefix),
			template.HTMLEscapeString(string(id)))
		return html, "Image", "", true
	}

	f, _, err := h.fs.OpenArtifact(ctx, id)
	if err != nil {
		return "", "", "", false
	}
	defer f.Close()

	// Choose the read budget by category. Hex needs only
	// the first 256 B; structured/text formats need more.
	limit := hexPreviewBytes
	if isStructuredText(mimeType) {
		limit = textPreviewBytes
	}
	buf := make([]byte, limit)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "", "", "", false
	}
	if n == 0 {
		return "", "", "", false
	}
	data := buf[:n]
	truncated := int64(n) < m.OriginalSize

	// Dispatch on MIME. Any failure falls back to hex so we
	// always show something rather than nothing.
	switch {
	case mimeIs(mimeType, "application/json"):
		if pretty, ok := tryFormatJSON(data); ok {
			return pretty, "JSON", truncatedNote(truncated), false
		}
	case mimeIs(mimeType, "application/xml") || mimeIs(mimeType, "text/xml"):
		if pretty, ok := tryFormatXML(data); ok {
			return pretty, "XML", truncatedNote(truncated), false
		}
	case mimeIs(mimeType, "text/csv"):
		if html, ok := tryFormatCSV(data); ok {
			return html, "CSV", truncatedNote(truncated), true
		}
	case mimeIs(mimeType, "text/plain") || mimeIs(mimeType, "text/markdown"):
		// Plain text: just show as-is.
		return string(data), "Text", truncatedNote(truncated), false
	}

	// Fallback: hex.
	return formatHexDump(data), "Hex (first 256 bytes)", "", false
}

// buildLocationViews assembles the rows for the artifact page's
// Locations panel — one row per tree this artifact appears in.
// Empty tree paths produce no row (that tree doesn't carry the
// artifact, e.g. ByPath="" for orphaned).
//
// Each URL points at the parent directory of the artifact's
// slot, not at the artifact file itself: clicking lands the
// user on the listing where the artifact's siblings are
// visible (the whole point of "show me where this lives").
//
// We always route service trees through _scrinium/<tree>/...
// regardless of which RootView is configured. That guarantees
// stable links: even if the daemon is started with
// RootView=byDate (so byDate is at the URL root), the by-path
// link still works via /_browse/_scrinium/by-path/...
func (h *Handler) buildLocationViews(locs projection.Locations) []locationView {
	servicePrefix := h.cfg.ServicePrefix

	roots := make([]string, 0, len(locs.Paths))
	for r := range locs.Paths {
		roots = append(roots, string(r))
	}
	sort.Strings(roots)

	out := make([]locationView, 0, len(roots))
	for _, r := range roots {
		path := locs.Paths[projection.RootView(r)]
		if path == "" {
			continue
		}
		if servicePrefix == "" {
			// Without a service prefix only the root view is mounted at
			// the bare prefix; we don't know which root that is here, so
			// surface each placement against it. The root view's link
			// resolves; others are informational.
			out = append(out, locationView{
				Tree: r,
				Path: path,
				URL:  parentURL(h.prefix+"/", path),
			})
			continue
		}
		// The URL segment matches the root name. by-orphaned is the one
		// intrinsic view whose mount segment ("orphaned") differs.
		seg := r
		if r == "by-orphaned" {
			seg = "orphaned"
		}
		base := h.prefix + "/" + servicePrefix + "/" + seg + "/"
		out = append(out, locationView{
			Tree: r,
			Path: path,
			URL:  parentURL(base, path),
		})
	}
	return out
}

// parentURL composes the URL pointing at the parent directory
// of `path` under `base`. If path has no directory component
// (e.g. "stats" at the tree root), the URL is base alone —
// landing the user at the root listing.
func parentURL(base, path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return base + path[:i+1]
	}
	return base
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

// previewMIME picks the MIME used to choose a preview format.
// vfsmeta is the authoritative source; filename extension is the
// fallback. Empty when neither yields anything — caller treats
// that as "use hex".
func previewMIME(m domain.Manifest) string {
	mimeType := ""
	name := ""
	if fs, ok, err := vfsmeta.Decode(m.Ext); err == nil && ok {
		mimeType = fs.MIME
		name = pathx.LastSegment(fs.Path)
	}
	if mimeType == "" {
		mimeType = inferMIME(name, "")
	}
	return mimeType
}

// mimeIs reports whether the given MIME (possibly with
// parameters like ";charset=utf-8") matches the bare type.
// Comparison is on the part before any semicolon.
func mimeIs(mimeType, want string) bool {
	base := mimeType
	if i := strings.IndexByte(base, ';'); i >= 0 {
		base = strings.TrimSpace(base[:i])
	}
	return base == want
}

// isStructuredText reports whether the MIME type benefits from
// the larger text-preview budget. Anything we render via JSON,
// XML, CSV, or plain-text paths counts; hex stays on its tiny
// 256 B budget so giant binaries don't pay needless I/O.
func isStructuredText(mimeType string) bool {
	switch {
	case mimeIs(mimeType, "application/json"),
		mimeIs(mimeType, "application/xml"),
		mimeIs(mimeType, "text/xml"),
		mimeIs(mimeType, "text/csv"),
		mimeIs(mimeType, "text/plain"),
		mimeIs(mimeType, "text/markdown"):
		return true
	}
	return false
}

// truncatedNote returns the user-visible label appended to the
// preview heading when we read fewer bytes than the artifact
// holds. Empty when the read covered everything.
func truncatedNote(truncated bool) string {
	if !truncated {
		return ""
	}
	return fmt.Sprintf("truncated at %d KiB", textPreviewBytes/1024)
}

// tryFormatJSON re-formats raw JSON bytes with two-space indent.
// Returns (pretty, true) on success, ("", false) on parse error
// (caller falls back to hex preview).
//
// Truncation caveat: we may have read only the first 64 KiB of
// a larger document, which mid-object is invalid JSON.
// Unmarshal will fail in that case — we accept the fallback to
// hex rather than try heroic prefix-recovery.
func tryFormatJSON(data []byte) (string, bool) {
	var any interface{}
	if err := json.Unmarshal(data, &any); err != nil {
		return "", false
	}
	out, err := json.MarshalIndent(any, "", "  ")
	if err != nil {
		return "", false
	}
	return string(out), true
}

// tryFormatXML re-emits the XML with line breaks and indent.
// Same truncation caveat as JSON: a truncated document is not
// well-formed XML and parsing fails — we fall back to hex.
//
// Implementation uses encoding/xml.Decoder + xml.Encoder with
// indent. We don't validate against any schema; we only
// reformat structurally valid input.
func tryFormatXML(data []byte) (string, bool) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	enc.Indent("", "  ")
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false
		}
		if err := enc.EncodeToken(tok); err != nil {
			return "", false
		}
	}
	if err := enc.Flush(); err != nil {
		return "", false
	}
	return out.String(), true
}

// tryFormatCSV parses CSV bytes and renders them as an HTML
// table. The first row is treated as the header (rendered in
// <thead>); subsequent rows go in <tbody>. We use a permissive
// reader (FieldsPerRecord=-1) so ragged rows still render —
// extra cells appear, missing cells stay empty.
//
// Truncation caveat: a truncation that splits a row mid-field
// produces an io.ErrUnexpectedEOF. We accept whatever rows
// came back before the error and ignore the trailing fragment;
// users see "truncated" in the heading.
//
// Output is template.HTML — pre-rendered. The artifact-page
// template inserts it verbatim, but we still escape every cell
// via html/template.HTMLEscapeString to avoid HTML injection
// from CSV cells like "<script>".
func tryFormatCSV(data []byte) (string, bool) {
	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var rows [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Salvage what we have. Truncation often shows
			// up here as ErrFieldCount or ErrBareQuote on
			// the last partial row.
			if len(rows) == 0 {
				return "", false
			}
			break
		}
		rows = append(rows, rec)
	}
	if len(rows) == 0 {
		return "", false
	}

	var b strings.Builder
	b.WriteString(`<table class="csv">`)

	// First row → thead; even one-row inputs get a thead so
	// the styling is consistent.
	b.WriteString("<thead><tr>")
	for _, cell := range rows[0] {
		fmt.Fprintf(&b, "<th>%s</th>", template.HTMLEscapeString(cell))
	}
	b.WriteString("</tr></thead>")

	if len(rows) > 1 {
		b.WriteString("<tbody>")
		for _, row := range rows[1:] {
			b.WriteString("<tr>")
			for _, cell := range row {
				fmt.Fprintf(&b, "<td>%s</td>", template.HTMLEscapeString(cell))
			}
			b.WriteString("</tr>")
		}
		b.WriteString("</tbody>")
	}
	b.WriteString("</table>")
	return b.String(), true
}

// artifactPageData is the artifact page's render payload.
type artifactPageData struct {
	Layout

	// Identity & storage are flat tables of label/value rows.
	// We render them as ordered slices instead of maps so the
	// row order is deterministic across reloads.
	Identity []labelValue
	Storage  []labelValue

	// Locations lists this artifact's placement across each
	// view tree. Each row links to the parent directory in
	// browse — clicking jumps the user to where the artifact
	// lives in that tree, with its siblings visible. Empty
	// for the few synthesised artifacts that have no records.
	Locations []locationView

	// Pipeline is the per-stage transform list. Empty for
	// artifacts that went straight to disk untransformed.
	Pipeline []pipelineStageView

	// Related is the list of dedup siblings — other artifacts
	// pointing at the same BlobRef. Empty (nil) when this
	// blob is unique; the template hides the section. Each
	// entry links back to that artifact's info page.
	Related []relatedView

	// Schema renders one of:
	//   - SchemaHTML — when a registered decoder claimed one of
	//     the Ext schema keys. Trusted HTML, the decoder owns it.
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

	// Preview is a content-aware peek at the artifact's
	// payload, shown inside a <details> just above the
	// raw manifest. The kind drives both the section's
	// heading and the rendering style (CSV → table, JSON →
	// pretty pre, etc.). Empty kind hides the section.
	Preview     string
	PreviewKind string
	PreviewNote string // e.g. "truncated at 64 KB"

	// PreviewIsTable signals that Preview is already rendered
	// HTML (a CSV table, an <img>); the template inserts it
	// verbatim without escaping. Other kinds use the
	// plain-text path.
	PreviewIsTable bool

	// PreviewHTML carries the safe template.HTML for the
	// table/image cases. We use a separate field so
	// html/template's auto-escaping keeps protecting the
	// plain-text variants.
	PreviewHTML template.HTML

	// PreviewOpen makes the <details> default to expanded.
	// True for content-meaningful previews (image, JSON, CSV,
	// text); false for hex, where the user opts in only when
	// they need to inspect bytes.
	PreviewOpen bool
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

// locationView is one row of the Locations panel. Tree is the
// human-readable tree label ("by-path", "by-date" etc.); Path
// shows the artifact's slot in that tree; URL points at the
// parent directory in browse so the user lands on the listing
// with the artifact's siblings visible.
type locationView struct {
	Tree string
	Path string
	URL  string
}

// relatedView is one row of the Related-artifacts table. Each
// row links to the sibling's info page; the URL is built once
// up front so the template stays free of helpers.
type relatedView struct {
	URL       string
	Path      string // empty → "(orphaned)"
	SessionID string
	CreatedAt string // pre-formatted RFC3339
	IsOrphan  bool
}

// buildArtifactData fills the template payload. Errors are
// returned but only as diagnostics — the data is always
// renderable, errors signal partial degradation.
func (h *Handler) buildArtifactData(ctx context.Context, m domain.Manifest) (artifactPageData, error) {
	data := artifactPageData{
		Layout: h.layout(),
	}

	data.Identity = []labelValue{
		{Label: "ArtifactID", Value: string(m.ArtifactID), Mono: true},
		{Label: "SessionID", Value: orDash(string(m.SessionID)), Mono: m.SessionID != ""},
		{Label: "CreatedAt", Value: m.CreatedAt.UTC().Format(time.RFC3339)},
		{Label: "RetentionUntil", Value: formatTimeOrDash(m.RetentionUntil)},
	}

	// Locations panel — where the artifact appears in each
	// tree, linked to the parent directory in browse so the
	// user sees siblings.
	if locs, ok, err := h.fs.LookupLocations(ctx, m.ArtifactID); err == nil && ok {
		data.Locations = h.buildLocationViews(locs)
	}

	blobRefValue := string(m.PrimaryBlobRef())
	if blobRefValue == string(m.ContentHash) && len(m.Pipeline) == 0 {
		// Pipeline-empty artifacts have BlobRef == ContentHash by
		// construction (the same bytes get hashed twice along
		// the put path). Surface this so the user doesn't read
		// it as a coincidence.
		blobRefValue = blobRefValue + " (same as ContentHash, no pipeline)"
	}
	data.Storage = []labelValue{
		{Label: "BlobRef", Value: blobRefValue, Mono: true},
		{Label: "ContentHash", Value: string(m.ContentHash), Mono: true},
		{Label: "OriginalSize", Value: humanize.BytesWithRaw(m.OriginalSize)},
		{Label: "Layout", Value: orDash(m.LayoutHeader.BlobStorage)},
		{Label: "KeyID", Value: orDash(m.KeyID), Mono: m.KeyID != ""},
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

	// Related artifacts — other manifests that share this
	// blob. Best-effort: a lookup error doesn't block the
	// page; we just don't show siblings.
	if related, err := h.fs.LookupRelated(ctx, m.PrimaryBlobRef(), m.ArtifactID); err == nil {
		for _, ra := range related {
			view := relatedView{
				URL:       h.prefix + "/_artifact/" + string(ra.ArtifactID),
				Path:      ra.Path,
				SessionID: string(ra.SessionID),
				CreatedAt: ra.CreatedAt.UTC().Format(time.RFC3339),
				IsOrphan:  ra.Path == "",
			}
			data.Related = append(data.Related, view)
		}
	}

	// Schema rendering targets the engine-custom index block (Ext
	// per ADR-54), a JSON object keyed by schema name where vfsmeta
	// and similar schemas live. Three branches:
	//
	//   1. Ext is empty → no Schema section.
	//   2. A key in Ext matches a registered decoder → render
	//      through the decoder (it pulls its own key from Ext); on
	//      error, fall back to JSON with the error noted.
	//   3. Otherwise → pretty JSON view, no schema highlighted.
	if len(m.Ext) > 0 {
		var dec SchemaDecoder
		for _, k := range extKeys(m.Ext) {
			if d, ok := h.decoders[k]; ok {
				dec, data.SchemaKind = d, k
				break
			}
		}
		if dec != nil {
			rendered, err := dec.Render(m.Ext)
			if err != nil {
				data.SchemaError = err.Error()
				data.SchemaJSON = prettyJSON(m.Ext)
			} else {
				data.SchemaHTML = rendered
			}
		} else {
			data.SchemaJSON = prettyJSON(m.Ext)
		}
	}

	// Raw manifest JSON. We construct a small struct mirroring
	// the wire shape (ArtifactID is intentionally absent — it's
	// derived, not serialised, per docs §7.4).
	raw, err := json.MarshalIndent(struct {
		SessionID      string                 `json:"session_id,omitempty"`
		CreatedAt      time.Time              `json:"created_at"`
		ContentHash    domain.ContentHash     `json:"content_hash,omitempty"`
		OriginalSize   int64                  `json:"original_size"`
		BlobRef        domain.BlobRef         `json:"blob_ref,omitempty"`
		LayoutHeader   domain.LayoutHeader    `json:"layout_header"`
		Pipeline       []domain.PipelineStage `json:"pipeline,omitempty"`
		RetentionUntil time.Time              `json:"retention_until,omitempty"`
		KeyID          string                 `json:"key_id,omitempty"`
		Ext            json.RawMessage        `json:"ext,omitempty"`
		Usr            json.RawMessage        `json:"usr,omitempty"`
	}{
		SessionID:      string(m.SessionID),
		CreatedAt:      m.CreatedAt,
		ContentHash:    m.ContentHash,
		OriginalSize:   m.OriginalSize,
		BlobRef:        m.PrimaryBlobRef(),
		LayoutHeader:   m.LayoutHeader,
		Pipeline:       m.Pipeline,
		RetentionUntil: m.RetentionUntil,
		KeyID:          m.KeyID,
		Ext:            m.Ext,
		Usr:            m.Usr,
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
