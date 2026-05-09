package core

import (
	"fmt"

	"github.com/rkurbatov/scrinium/engine/domain"
	"github.com/rkurbatov/scrinium/engine/errs"
)

// dispatchManifestType returns nil if m is a regular Blob manifest
// the engine should process inline, or the appropriate sentinel /
// wrapped error otherwise. Get, Delete and Verify share this table:
// Blob continues, TOC awaits the chunker decorator, Pack is
// engine-internal (invisible to clients) and surfaces as
// "not found", everything else is unknown.
//
// op is the operation name (e.g. "core.Get") used to build the
// error message — there is no caller-side wrapping needed.
func dispatchManifestType(m domain.Manifest, op string) error {
	switch m.Type {
	case domain.ManifestTypeBlob:
		return nil
	case domain.ManifestTypeTOC:
		return fmt.Errorf("%w: %s on ManifestTypeTOC requires the chunker decorator",
			errs.ErrNotImplemented, op)
	case domain.ManifestTypePack:
		// §3.1: pack manifests are engine-internal, invisible to
		// clients. Collapse to ErrArtifactNotFound so client code
		// does not have to special-case them.
		return errs.ErrArtifactNotFound
	default:
		return fmt.Errorf("%s: unknown manifest type %q", op, m.Type)
	}
}
