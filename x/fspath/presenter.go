package fspath

import (
	"encoding/json"
	"fmt"
	"time"

	"scrinium.dev/domain/vfsmeta"
	"scrinium.dev/present"
)

// PresentedSchemas implements present.SchemaPresenter (ADR-109): fspath is
// the vfsmeta extension, so it presents the "vfsmeta" schema. The decode
// stays in domain/vfsmeta (the schema owns its shape); the extension owns
// the presentation. The output is format-neutral — a surface renders it.
func (e *CustomIndex) PresentedSchemas() []present.Schema {
	return []present.Schema{{
		Key:     vfsmeta.Key,
		Present: presentVfsmeta,
	}}
}

// presentVfsmeta decodes the vfsmeta block of ext and lays it out as a
// Representation. ok=false when ext carries no recognised vfsmeta payload
// (e.g. a future schema version) so the surface falls back to raw JSON.
// Fields mirror the prior webview rendering: path, mode (octal), uid, gid,
// mtime (when set) and mime (when set).
func presentVfsmeta(ext json.RawMessage) (present.Representation, bool, error) {
	fs, ok, err := vfsmeta.Decode(ext)
	if err != nil {
		return present.Representation{}, false, fmt.Errorf("vfsmeta.Decode: %w", err)
	}
	if !ok {
		return present.Representation{}, false, nil
	}

	fields := []present.Field{
		{Label: "Path", Value: fs.Path, Kind: present.Path, Ref: fs.Path},
		{Label: "Mode", Value: fmt.Sprintf("%#o", fs.Mode), Kind: present.Mode},
		{Label: "UID", Value: fmt.Sprintf("%d", fs.UID), Kind: present.Number},
		{Label: "GID", Value: fmt.Sprintf("%d", fs.GID), Kind: present.Number},
	}
	if !fs.ModTime.IsZero() {
		fields = append(fields, present.Field{
			Label: "ModTime",
			Value: fs.ModTime.UTC().Format(time.RFC3339),
			Kind:  present.Time,
		})
	}
	if fs.MIME != "" {
		fields = append(fields, present.Field{Label: "MIME", Value: fs.MIME, Kind: present.Text})
	}

	return present.Representation{Title: "Filesystem", Fields: fields}, true, nil
}
