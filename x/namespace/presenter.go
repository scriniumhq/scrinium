package namespace

import (
	"context"
	"encoding/json"

	"scrinium.dev/present"
)

// PresentedSchemas implements present.SchemaPresenter (ADR-109): namespace
// owns the "nsid" schema, so it presents it. When a registry is wired the
// nsid is resolved to its current human name; otherwise the verbatim nsid
// is shown.
func (e *Index) PresentedSchemas() []present.Schema {
	return []present.Schema{{
		Key:     nsidField,
		Present: e.presentNSID,
	}}
}

// presentNSID lays out the nsid block of ext. ok=false when ext carries no
// nsid (the artifact belongs to no namespace) so the surface falls back to
// raw JSON. The nsid is resolved to its current name via the registry when
// one is wired (mirroring the by-namespace view's Label); a miss shows the
// verbatim nsid only.
func (e *Index) presentNSID(ext json.RawMessage) (present.Representation, bool, error) {
	id, ok, err := nsidOf(ext)
	if err != nil {
		return present.Representation{}, false, err
	}
	if !ok {
		return present.Representation{}, false, nil
	}

	fields := []present.Field{
		{Label: "Namespace ID", Value: string(id), Kind: present.Ref},
	}
	if e.reg != nil {
		if view, err := e.reg.Load(context.Background()); err == nil {
			if name, ok := view.Name(id); ok {
				fields = append(fields, present.Field{Label: "Name", Value: name, Kind: present.Text})
			}
		}
	}

	return present.Representation{Title: "Namespace", Fields: fields}, true, nil
}
