// Package extension is the client-level umbrella over Scrinium's
// extension axes. An Extension is one whole unit a host installs; the
// client routes its parts to their levels (a CustomIndex into the
// StoreIndex, and — as they are wired — a wrapper onto the store, an
// agent onto the scheduler) via Use, so the host reasons about
// extensions as wholes, not about the index/store axes underneath
// (ADR-88, "Клиент = дескриптор-управляемая авто-разводка").
//
// Manual per-axis wiring (storeIndex.Register, store.Wrap,
// scheduler.Add) yields the identical result (ADR-88, Principle 12);
// Use is the one-call convenience over the same primitives.
package extension

import (
	"context"
	"fmt"

	"scrinium.dev/engine/customindex"
)

// Descriptor identifies an Extension as a whole and is the unit the
// client surfaces (e.g. on a stats page). It is deliberately free of
// the index/store axis detail the extension occupies underneath.
type Descriptor struct {
	// Name is the extension's stable identifier (e.g. "fs").
	Name string
}

// Extension is one whole, client-level unit composed of axis parts.
// Each part accessor returns (part, true) when the extension occupies
// that axis and (zero, false) otherwise. Today only the index axis
// (CustomIndex) is modelled; the behavioural (wrapper) and agent axes
// are added to this interface as they are wired.
type Extension interface {
	// Descriptor reports the extension's identity.
	Descriptor() Descriptor

	// CustomIndex is the index-axis part (Tier 2), registered into the
	// StoreIndex. Returns (nil, false) when the extension has none.
	CustomIndex() (customindex.CustomIndex, bool)
}

// Target is the seam the assembler implements so Use can route an
// extension's parts to their levels without this package importing the
// engine wiring. Each method installs one axis.
type Target interface {
	// RegisterCustomIndex installs ci into the backing StoreIndex.
	RegisterCustomIndex(ctx context.Context, ci customindex.CustomIndex) error
}

// Use installs e as one whole: it reads the descriptor and routes each
// occupied axis part to its level through t. The result is identical to
// wiring the parts by hand (ADR-88, Principle 12).
func Use(ctx context.Context, t Target, e Extension) error {
	if ci, ok := e.CustomIndex(); ok {
		if err := t.RegisterCustomIndex(ctx, ci); err != nil {
			return fmt.Errorf("extension %q: register custom index: %w", e.Descriptor().Name, err)
		}
	}
	return nil
}
