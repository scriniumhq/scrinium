package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"scrinium.dev/projection/fsmeta"
)

// fsmetaDecoder renders Manifest.Metadata payloads that match
// the scrinium.fs/v1 schema (the projection/fsmeta marker).
// Registered with the web Handler at daemon startup so the
// artifact details page surfaces filesystem metadata as a
// nice table rather than raw JSON.
//
// Living in the cmd package, not in web/, is deliberate: web
// stays schema-agnostic; hosts wire whichever decoders they
// understand.
type fsmetaDecoder struct{}

func (fsmetaDecoder) Marker() string { return fsmeta.Marker }

func (fsmetaDecoder) Render(raw json.RawMessage) (template.HTML, error) {
	fs, ok, err := fsmeta.Decode(raw)
	if err != nil {
		return "", fmt.Errorf("fsmeta.Decode: %w", err)
	}
	if !ok {
		// Marker matched at the dispatch site but Decode says
		// no — likely a future v2 we don't understand. Let the
		// caller fall through to JSON view.
		return "", fmt.Errorf("fsmeta: payload not recognised by v1 decoder")
	}

	var b strings.Builder
	b.WriteString(`<table class="schema-fsmeta"><tbody>`)
	row(&b, "Path", template.HTMLEscapeString(fs.Path), true)
	row(&b, "Mode", fmt.Sprintf("%#o", fs.Mode), true)
	row(&b, "UID", fmt.Sprintf("%d", fs.UID), false)
	row(&b, "GID", fmt.Sprintf("%d", fs.GID), false)
	if !fs.ModTime.IsZero() {
		row(&b, "ModTime", fs.ModTime.UTC().Format(time.RFC3339), true)
	}
	if fs.MIME != "" {
		row(&b, "MIME", template.HTMLEscapeString(fs.MIME), true)
	}
	b.WriteString(`</tbody></table>`)
	return template.HTML(b.String()), nil
}

// row appends one <tr> with label / value cells. mono toggles
// monospace rendering for path-shaped or octal-shaped values.
func row(b *strings.Builder, label, value string, mono bool) {
	cls := "value"
	if mono {
		cls += " mono"
	}
	fmt.Fprintf(b, `<tr><td class="label">%s</td><td class="%s">%s</td></tr>`,
		template.HTMLEscapeString(label), cls, value)
}
