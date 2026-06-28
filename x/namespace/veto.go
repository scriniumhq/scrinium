package namespace

import (
	"context"
	"encoding/json"
	"fmt"

	"scrinium.dev/engine/systemstore"
	"scrinium.dev/extension"
)

// ValidateSystemWrite is the namespace extension's pre-write veto (ADR-108).
// It guards the one scoped system artifact the extension owns — the registry —
// and rejects any write that is not a structurally valid {id → name} map or
// that would break the registry's invariants: a malformed name, or a name
// claimed by more than one id. It is defense-in-depth — Registry.Create
// already enforces these on the managed path, but a direct scoped Put would
// bypass it, and the veto closes that gap so the persisted map is never
// internally inconsistent.
//
// The check reads only the proposed bytes; it needs no committed state, so
// current is unused and the validator never writes through it (no recursion).
// Any other artifact name is accepted untouched — the namespace scope holds
// nothing but the registry.
func (e *Extension) ValidateSystemWrite(_ context.Context, name string, proposed []byte, _ systemstore.Store) error {
	if name != registryArtifact {
		return nil
	}

	var snap snapshot
	if err := json.Unmarshal(proposed, &snap); err != nil {
		return fmt.Errorf("namespace registry: rejected write: not a valid {id → name} map: %w", err)
	}

	seen := make(map[string]NamespaceID, len(snap.Entries))
	for id, nsName := range snap.Entries {
		if err := validateName(nsName); err != nil {
			return fmt.Errorf("namespace registry: rejected write: entry %q: %w", id, err)
		}
		if other, dup := seen[nsName]; dup {
			return fmt.Errorf("namespace registry: rejected write: name %q claimed by both %q and %q", nsName, other, id)
		}
		seen[nsName] = id
	}
	return nil
}

// Compile-time conformance: the namespace extension vetoes writes to its own
// scoped system artifacts. The assembler installs it on the scope it hands
// back (ADR-108).
var _ extension.SystemArtifactValidator = (*Extension)(nil)
