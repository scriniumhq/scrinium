// Package present is the presentation-plane leaf contract (ADR-109): a
// format-neutral representation of an Ext-schema block, plus the optional
// SchemaPresenter capability by which a schema's owning extension exposes
// how to present it.
//
// No HTML or any output format lives here — a surface (webview, a TUI, a
// CLI "explain") renders a Representation into its own format. present has
// no dependency on the index engine; customindex does not import it
// (ADR-109 INV-1), so the assertion ci.(present.SchemaPresenter) at the
// composition root introduces no cycle.
package present

// Representation is a format-neutral rendering of one Ext-schema block: a
// titled list of labelled fields, in display order. A surface turns it
// into HTML, text or JSON; the contract itself carries no format.
type Representation struct {
	// Title names the schema for a human, e.g. "Filesystem".
	Title string
	// Fields are the schema's values, in the order they should display.
	Fields []Field
}

// Field is one labelled value with a presentation hint.
type Field struct {
	// Label is the human name of the value, e.g. "Path", "Owner".
	Label string
	// Value is the already-formatted scalar the surface shows verbatim,
	// e.g. "0100644", "1000". Formatting (octal, RFC3339, …) is the
	// presenter's job, not the surface's.
	Value string
	// Kind hints how a surface may render Value (monospace a path,
	// linkify a ref, …). A surface is free to ignore it.
	Kind Kind
	// Ref is an optional link target — a path or artifact id — a surface
	// may turn into a link when Kind is Path or Ref. Empty otherwise.
	Ref string
}

// Kind is a presentation hint for a Field's value. It names no format; a
// surface maps it to its own rendering (monospace, link, …).
type Kind uint8

const (
	Text   Kind = iota // a plain prose scalar
	Number             // a count or id-like integer
	Path               // a slash-separated logical path
	Ref                // a cross-reference, e.g. an artifact id
	Mode               // POSIX mode bits
	Time               // a timestamp
	Bytes              // a byte size
)
