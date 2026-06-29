package namespace

import (
	"context"
	"errors"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/customindex"
	"scrinium.dev/engine/systemstore"
	"scrinium.dev/engine/wrapper"
	"scrinium.dev/errs"
	"scrinium.dev/extension"
)

// extensionName is the extension's stable identity. It is also the scope
// token of its system artifacts ("extension.namespace.*") and the
// proj_ext ext_name of its index — keeping all three the same name is
// what ties the registry, the projection, and Walk(ns) together.
const extensionName = "namespace"

// Extension is the namespace extension as one whole (ADR-79/88/99). It
// occupies the nsid CustomIndex (Tier 2) and ALWAYS the behavioral wrapper
// axis (Tier 3) — the open wrapper when unbound, the scoped wrapper when
// bound — because the per-call WithNamespace works only through a wrapper.
// It owns a registry of {NamespaceID → name} in its own scoped
// system-artifact space, and brings no agent (the reactive namespace-sync
// worker is the multistore plane, M5.4, not here).
type Extension struct {
	idx      *Index
	registry *Registry

	// bound is the NamespaceID the scoped wrapper pins writes and listing
	// to, set by NewScoped. scoped selects which wrapper Wrapper() returns:
	// the scoped wrapper (bound) or the open wrapper (unbound). Either way
	// the wrapper axis is occupied.
	bound  NamespaceID
	scoped bool
}

// New builds the namespace extension over a store's SystemStore. It
// confines itself to the "extension.namespace." scope internally, so the
// caller hands it the unscoped SystemStore (e.g. store.System()) and the
// extension's artifact space cannot drift from its name.
//
// New is the explicit-host path (the caller already holds a store). The
// auto-wired path is NewExtension, whose registry is bound later through
// UseEnv (ADR-101 §4); both converge on the same scoped store. New itself
// scoping an unscoped handle is the older pattern — the assembler now
// builds and confines the scoped store and delivers it via Env, so
// nothing outside the extension ever holds the unscoped one.
func New(sys systemstore.Store) (*Extension, error) {
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

// NewExtension builds the namespace extension as one whole with NO store
// handle, for the blank-import / RegisterExtension path (ADR-63), exactly
// like fspath.NewExtension. Its registry has no durable backing yet; the
// assembler delivers a scoped SystemStore through UseEnv once the store is
// open (ADR-100/101 §4), which binds the registry. Until then the registry
// rejects reads/writes (so the by-namespace view simply labels nodes with
// the verbatim nsid rather than a name — the documented no-durable-source
// fallback).
func NewExtension() extension.Extension {
	reg := NewRegistry(nil)
	return &Extension{
		idx:      NewIndex(reg),
		registry: reg,
	}
}

// UseEnv binds the extension's registry to the scoped SystemStore the
// assembler delivers after the store opens (extension.Receiver, ADR-101
// §4). Idempotent: re-binding replaces the backing, so it is safe whether
// the extension was built by NewExtension (deferred) or New (already
// scoped — the delivered handle then becomes authoritative).
func (e *Extension) UseEnv(env extension.Env) error {
	if env.SystemStore == nil {
		return fmt.Errorf("namespace: UseEnv with nil scoped system store")
	}
	e.registry.bind(env.SystemStore)
	return nil
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
func NewScoped(ctx context.Context, sys systemstore.Store, scopedNamespace string) (*Extension, error) {
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

// MemberLister is the narrow read capability DeleteNamespace needs to
// decide whether a namespace still has members: the extension-agnostic
// nsid-projection query (proj_ext). A *store.Store (or the assembled data
// store) satisfies it — it is the same WalkByExt the scoped wrapper lists
// through. Kept narrow so the guard takes no dependency on the full store.
type MemberLister interface {
	WalkByExt(ctx context.Context, extName, field, value string, cb func(domain.Manifest) error) error
}

// ErrNamespaceNotEmpty is returned by DeleteNamespace when the namespace
// still has at least one member (ADR-96 K3). The default policy is
// Managed/refuse: a non-empty namespace is not deleted without an explicit
// reassign (AutoReassign is the multistore cascade, M5.4, not wired here).
// The caller empties or reassigns its artifacts first.
var ErrNamespaceNotEmpty = errors.New("namespace: not empty")

// DeleteNamespace removes the namespace named name, applying the K3 guard
// (ADR-96): it refuses with ErrNamespaceNotEmpty when any artifact is still
// stamped with the namespace's nsid. members is the data plane the
// membership check queries through the nsid projection; the raw, unguarded
// map delete stays on Registry.Delete for the future reassign cascade. An
// unknown name returns errs.ErrArtifactNotFound (from the registry).
//
// This is the guarded admin delete; Create and List have no cross-index
// invariant and stay on Registry()/View. Membership is checked, not
// counted: the walk stops at the first member.
func (e *Extension) DeleteNamespace(ctx context.Context, members MemberLister, name string) error {
	view, err := e.registry.Load(ctx)
	if err != nil {
		return err
	}
	id, ok := view.Resolve(name)
	if !ok {
		return fmt.Errorf("namespace %q: %w", name, errs.ErrArtifactNotFound)
	}
	found := false
	errStop := errors.New("stop")
	werr := members.WalkByExt(ctx, indexName, nsidField, string(id), func(domain.Manifest) error {
		found = true
		return errStop
	})
	if werr != nil && !errors.Is(werr, errStop) {
		return fmt.Errorf("namespace %q: membership check: %w", name, werr)
	}
	if found {
		return fmt.Errorf("namespace %q: %w", name, ErrNamespaceNotEmpty)
	}
	return e.registry.Delete(ctx, name)
}

// Descriptor reports the extension's identity.
func (e *Extension) Descriptor() extension.Descriptor {
	return extension.Descriptor{Name: extensionName}
}

// CustomIndex is the index-axis part: the nsid projection.
func (e *Extension) CustomIndex() (customindex.CustomIndex, bool) {
	return e.idx, true
}

// Wrapper reports the data-plane wrapper — MANDATORY for the namespace
// extension, which always occupies the behavioral axis (ADR-99): the
// per-call WithNamespace works only through a wrapper, so the extension
// supplies one in both modes.
//
//   - Unbound (New/NewExtension) → the OPEN wrapper (A): a Put may name any
//     namespace via WithNamespace, which the wrapper resolves and stamps
//     into Ext; without it the Put is unstamped. Reads see the whole store.
//   - Bound (NewScoped) → the SCOPED wrapper (B): writes/listing are pinned
//     to the bound namespace, and passing WithNamespace is a conflict
//     (ErrNamespaceConflict).
func (e *Extension) Wrapper() (wrapper.Factory, bool) {
	if e.scoped {
		return scopedFactory{nsid: e.bound}, true
	}
	return openFactory{reg: e.registry}, true
}

// Agents reports no paired background workers: namespace-sync is the
// multistore plane (M5.4), not this single-store extension.
func (e *Extension) Agents() []extension.Agent { return nil }

// Compile-time conformance: the namespace extension is a whole Extension
// that also receives the late-bound Env (its scoped SystemStore).
var _ extension.Extension = (*Extension)(nil)
var _ extension.Receiver = (*Extension)(nil)
