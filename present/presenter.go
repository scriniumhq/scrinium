package present

import "encoding/json"

// SchemaPresenter is the optional capability (ADR-109) by which a schema's
// owning extension exposes how to present its Ext blocks. It is discovered
// by type assertion from a registered CustomIndex — exactly like
// customindex.ViewProvider — so an extension that presents no schema simply
// does not implement it. Presentation is a distinct plane from
// storage/index/view: the knowledge "how to show this schema" belongs to
// the schema's owner, and any surface consumes it.
type SchemaPresenter interface {
	// PresentedSchemas returns the Ext schemas this extension presents,
	// each keyed by its Ext key (e.g. "vfsmeta", "nsid").
	PresentedSchemas() []Schema
}

// Schema pairs an Ext schema key with a presenter for that schema's block.
// Present receives the whole Ext object and extracts its own key — the
// same contract the schema's own Decode already uses.
type Schema struct {
	// Key is the Ext schema key this presenter handles, e.g. "vfsmeta".
	Key string
	// Present turns an artifact's Ext into a Representation of this
	// schema's block. ok=false (with a zero Representation) means the
	// block is absent or unrecognised — the surface falls back to raw
	// JSON; err is reserved for a genuine decode failure.
	Present func(ext json.RawMessage) (rep Representation, ok bool, err error)
}

// Registry maps an Ext schema key to its presenter. It is assembled at the
// composition root (L3) from the installed extensions' SchemaPresenter
// capabilities and handed to surfaces (L4). It is deliberately NOT part of
// the projection Config — the projection does not render (ADR-109 INV-5).
type Registry map[string]Schema

// Lookup returns the presenter registered for an Ext schema key, if any.
func (r Registry) Lookup(key string) (Schema, bool) {
	s, ok := r[key]
	return s, ok
}
