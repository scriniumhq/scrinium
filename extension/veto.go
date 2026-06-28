package extension

import (
	"context"

	"scrinium.dev/engine/systemstore"
)

// SystemArtifactValidator is the optional pre-write veto an extension may
// implement to guard its own scoped system artifacts (ADR-108). The assembler
// detects it by assertion — like the CustomIndex / Wrapper / Receiver axes —
// and installs it on the extension's own ScopedSystemStore. Only the owning
// extension's scope carries the veto; another extension's scope has none, and
// the engine's own artifacts are never routed through it.
//
// ValidateSystemWrite runs synchronously inside Put, before the write reaches
// the backing store, so a rejection is atomic: a non-nil error aborts the Put
// and nothing is written.
//
//   - name is the LOCAL artifact name (no "extension.<name>." prefix) — the
//     same short name the extension passes to Put.
//   - proposed is the inline payload about to be written, buffered into memory
//     (system payloads are small). It is nil for an external-ref artifact,
//     which carries no inline body to inspect.
//   - current is the extension's own scoped store, for reading already-
//     committed artifacts by their local names (e.g. a uniqueness check). It
//     reflects the state before this write; the validator must not write
//     through it.
type SystemArtifactValidator interface {
	ValidateSystemWrite(ctx context.Context, name string, proposed []byte, current systemstore.Store) error
}
