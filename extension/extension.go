// Package extension is the client-level umbrella over Scrinium's
// extension axes. An Extension is one whole unit a host installs; the
// parts it carries are routed to their levels — a CustomIndex into the
// StoreIndex (Tier 2), a behavior wrapper onto the store's data plane
// (Tier 3), a paired background agent onto the scheduler (the optional
// "фоновый довесок"). The host reasons about extensions as wholes, not
// about the axes underneath (ADR-88, "Клиент = дескриптор-управляемая
// авто-разводка").
//
// The additive axes here follow ADR-88's per-store descriptor: behavior
// (wrapper), CustomIndex, and the optional background agent. (The
// multistore plane — custom routing, MultistoreIndex, namespace-sync —
// is a separate plane wired later; the capability-surface axis is a
// host type-assert and needs no umbrella plumbing.)
//
// Manual per-axis wiring (storeIndex.Register, store-level wrap,
// scheduler.Add) yields the identical result (ADR-88, Principle 12); Use
// is the one-call convenience over the same primitives, for a live
// target whose store and scheduler already exist. The assembler applies
// the parts directly at their construction phases (index before store
// open, wrapper at open, agent after the scheduler exists), which is the
// same per-part path.
package extension

import (
	"context"
	"fmt"

	"scrinium.dev/engine/customindex"
	"scrinium.dev/engine/wrapper"
)

// Descriptor identifies an Extension as a whole and is the unit the
// client surfaces (e.g. on a stats page). It is deliberately free of
// the axis detail the extension occupies underneath.
type Descriptor struct {
	// Name is the extension's stable identifier (e.g. "fs").
	Name string
}

// Agent is one paired background worker an extension brings along (the
// "фоновый довесок"): an agent Kind that must be registered in the agent
// registry (blank-import its package, as with drivers), optionally
// scheduled. Schedule is "" for manual-only (run via RunMaintenance), an
// interval string ("6h"), or a cron expression (requires cron.Enable).
// Config is handed to the agent's factory.
type Agent struct {
	Kind     string
	Schedule string
	Config   any
}

// Extension is one whole, client-level unit composed of axis parts. Each
// part accessor returns (part, true) when the extension occupies that
// axis and (zero, false / nil) otherwise.
type Extension interface {
	// Descriptor reports the extension's identity.
	Descriptor() Descriptor

	// CustomIndex is the index-axis part (Tier 2), registered into the
	// StoreIndex. Returns (nil, false) when the extension has none.
	CustomIndex() (customindex.CustomIndex, bool)

	// Wrapper is the behavior-axis part (Tier 3): a decorator factory
	// applied to the store's data plane. Returns (nil, false) when the
	// extension adds no behavior.
	Wrapper() (wrapper.Factory, bool)

	// Agents are the optional paired background workers the extension
	// brings (durable state + worker). Empty when none.
	Agents() []Agent
}

// Target is the seam a live host implements so Use can route an
// extension's parts to their levels without this package importing the
// engine wiring. Each method installs one axis.
type Target interface {
	// RegisterCustomIndex installs ci into the backing StoreIndex.
	RegisterCustomIndex(ctx context.Context, ci customindex.CustomIndex) error
	// Wrap applies f to the store's data plane.
	Wrap(f wrapper.Factory) error
	// AddAgent registers a paired background agent.
	AddAgent(a Agent) error
}

// Use installs e as one whole over a live target: it routes each
// occupied axis part to its level through t. The result is identical to
// wiring the parts by hand (ADR-88, Principle 12). It is for targets
// whose store and scheduler already exist; the assembler, which builds
// those in phases, applies the parts directly at each phase instead.
func Use(ctx context.Context, t Target, e Extension) error {
	name := e.Descriptor().Name
	if ci, ok := e.CustomIndex(); ok {
		if err := t.RegisterCustomIndex(ctx, ci); err != nil {
			return fmt.Errorf("extension %q: register custom index: %w", name, err)
		}
	}
	if f, ok := e.Wrapper(); ok {
		if err := t.Wrap(f); err != nil {
			return fmt.Errorf("extension %q: apply wrapper: %w", name, err)
		}
	}
	for _, a := range e.Agents() {
		if err := t.AddAgent(a); err != nil {
			return fmt.Errorf("extension %q: add agent %q: %w", name, a.Kind, err)
		}
	}
	return nil
}
