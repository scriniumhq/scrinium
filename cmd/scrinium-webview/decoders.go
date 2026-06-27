package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"scrinium.dev/domain/vfsmeta"
)

// vfsmetaDecoder renders the filesystem schema (domain/vfsmeta)
// from an artifact's Ext["vfsmeta"] block. Registered with the
// web Handler at daemon startup so the artifact details page
// surfaces filesystem metadata as a nice table rather than raw
// JSON.
//
// Living in the cmd package, not in web/, is deliberate: web
// stays schema-agnostic; hosts wire whichever decoders they
// understand.
type vfsmetaDecoder struct{}

func (vfsmetaDecoder) Key() string { return vfsmeta.Key }

func (vfsmetaDecoder) Render(ext json.RawMessage) (template.HTML, error) {
	fs, ok, err := vfsmeta.Decode(ext)
	if err != nil {
		return "", fmt.Errorf("vfsmeta.Decode: %w", err)
	}
	if !ok {
		// Key matched at the dispatch site but Decode says no —
		// likely a future version we don't understand. Let the
		// caller fall through to JSON view.
		return "", fmt.Errorf("vfsmeta: payload not recognised by v1 decoder")
	}

	var b strings.Builder
	b.WriteString(`<table class="schema-vfsmeta"><tbody>`)
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
