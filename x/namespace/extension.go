package namespace

import (
	"context"
	"fmt"

	"scrinium.dev/engine/customindex"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/wrapper"
	"scrinium.dev/extension"
)

// extensionName is the extension's stable identity. It is also the scope
// token of its system artifacts ("extension.namespace.*") and the
// proj_ext ext_name of its index — keeping all three the same name is
// what ties the registry, the projection, and Walk(ns) together.
const extensionName = "namespace"

// Extension is the namespace extension as one whole (ADR-79/88). It
// occupies a single axis — the nsid CustomIndex (Tier 2) — and owns a
// registry of {NamespaceID → name} kept in its own scoped system-artifact
// space. It adds no data-plane wrapper (namespace is a CustomIndex +
// registry, not a behaviour wrapper) and brings no agent (the reactive
// namespace-sync worker is the multistore plane, M5.4, not here).
type Extension struct {
	idx      *Index
	registry *Registry

	// bound is the NamespaceID this extension's data-plane wrapper pins
	// writes and listing to, set by NewScoped. scoped reports whether the
	// wrapper axis is occupied; an unbound extension (New) adds no wrapper.
	bound  NamespaceID
	scoped bool
}

// New builds the namespace extension over a store's SystemStore. It
// confines itself to the "extension.namespace." scope internally, so the
// caller hands it the unscoped SystemStore (e.g. store.System()) and the
// extension's artifact space cannot drift from its name.
func New(sys store.SystemStore) (*Extension, error) {
	scoped, err := extension.NewScopedSystemStore(extensionName, sys)
	if err != nil {
		return nil, err
	}
	reg := NewRegistry(scoped)
	return &Extension{
		idx:      NewIndex(reg),
		registry: reg,
	}, nil
}

// NewScoped builds the namespace extension pinned to one existing
// namespace, named by scopedNamespace which may be either a name or a
// NamespaceID ("scoped_namespace: name|id"). It resolves the value
// against the registry at construction and FAILS if it resolves to no
// existing namespace (Managed policy, ADR-96 K2). The bound id is held
// (not the name), so a later rename does not unbind the wrapper.
//
// A bound extension occupies the wrapper axis in addition to the index
// axis: its wrapper stamps the bound nsid into every Put's Ext and scopes
// Walk to that namespace.
func NewScoped(ctx context.Context, sys store.SystemStore, scopedNamespace string) (*Extension, error) {
	e, err := New(sys)
	if err != nil {
		return nil, err
	}
	view, err := e.registry.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("namespace: bind %q: load registry: %w", scopedNamespace, err)
	}
	id, ok := view.Bind(scopedNamespace)
	if !ok {
		return nil, fmt.Errorf("namespace: scoped_namespace %q does not resolve to an existing namespace", scopedNamespace)
	}
	e.bound = id
	e.scoped = true
	return e, nil
}

// Registry exposes the namespace registry. The host manages namespaces
// through it (Create/Delete/List) and the Put path resolves names to
// nsids through it before stamping Ext.
func (e *Extension) Registry() *Registry { return e.registry }

// Descriptor reports the extension's identity.
func (e *Extension) Descriptor() extension.Descriptor {
	return extension.Descriptor{Name: extensionName}
}

// CustomIndex is the index-axis part: the nsid projection.
func (e *Extension) CustomIndex() (customindex.CustomIndex, bool) {
	return e.idx, true
}

// Wrapper reports the data-plane wrapper. An unbound extension (New) adds
// none — namespace is a CustomIndex + registry, not a wrapper (ADR-79/88).
// A bound extension (NewScoped) occupies the behavioral wrapper axis: the
// returned factory pins writes and listing to the bound namespace.
func (e *Extension) Wrapper() (wrapper.Factory, bool) {
	if !e.scoped {
		return nil, false
	}
	return scopedFactory{nsid: e.bound}, true
}

// Agents reports no paired background workers: namespace-sync is the
// multistore plane (M5.4), not this single-store extension.
func (e *Extension) Agents() []extension.Agent { return nil }

// Compile-time conformance: the namespace extension is a whole Extension.
var _ extension.Extension = (*Extension)(nil)
