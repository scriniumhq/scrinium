package namespace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
	"scrinium.dev/domain"
	"scrinium.dev/engine/systemstore"
	"scrinium.dev/errs"
	"scrinium.dev/extension"
)

// NamespaceID is the stable identity of a namespace: a UUID minted at
// creation. It is the value stamped into an artifact's Ext (ADR-79); the
// human-facing name is an alias the registry maps to it, so renaming a
// namespace never rewrites a single artifact and the floating handle
// stays put (ADR-73).
type NamespaceID string

const (
	// registryArtifact is the scoped system-artifact name of the
	// {NamespaceID → name} map. Through the extension's scoped store it
	// resolves to "extension.namespace.registry" (ADR-85: a flat,
	// versioned named artifact).
	registryArtifact = "registry"

	// registryKeep is how many historical versions of the registry the
	// system store retains for audit/rollback; the active one is max(seq).
	registryKeep = 8
)

// errRegistryUnbound is returned by load/persist when the Registry has no
// scoped system store yet — i.e. an auto-wired extension (NewExtension)
// touched the registry before the assembler delivered its Env (ADR-101
// §4). It signals a wiring-order bug, not a missing-namespace condition,
// so it is kept distinct from errs.ErrArtifactNotFound.
var errRegistryUnbound = errors.New("namespace registry: no scoped system store bound (extension env not delivered)")

// Registry is the per-store {NamespaceID → name} map (ADR-79/96),
// persisted as a versioned system artifact through the extension's scoped
// store. Resolution (name → id) drives both Walk and the Put-time nsid
// stamp; the map is the single source of truth for which namespaces exist.
//
// Mutations are read-modify-write over the whole map and publish a new
// version (active = max seq). Namespace management is admin-driven and
// effectively serial (ADR-96: the synchronous path only enqueues), so a
// last-writer-wins version is acceptable here — this is not a hot path.
type Registry struct {
	sys *extension.ScopedSystemStore
}

// NewRegistry binds a Registry to an extension's scoped system store. sys
// may be nil — the auto-wired extension (NewExtension) constructs the
// Registry before the store opens and binds the real scoped store later
// via bind, when the assembler delivers the Env (ADR-101 §4). A Registry
// with no backing rejects load/persist with a clear error.
func NewRegistry(sys *extension.ScopedSystemStore) *Registry {
	return &Registry{sys: sys}
}

// bind attaches (or replaces) the scoped system store. Called from
// Extension.UseEnv once the assembler delivers the post-open Env.
func (r *Registry) bind(sys *extension.ScopedSystemStore) { r.sys = sys }

// View is an immutable, resolved snapshot of the registry for lookups.
// Hold one for a batch of resolutions rather than reloading per call.
type View struct {
	byID   map[NamespaceID]string
	byName map[string]NamespaceID
}

// Entry is one namespace as reported by View.List.
type Entry struct {
	ID   NamespaceID
	Name string
}

// Load reads the active registry and returns a resolved View. An absent
// registry (none created yet) yields an empty View, not an error.
func (r *Registry) Load(ctx context.Context) (*View, error) {
	snap, err := r.load(ctx)
	if err != nil {
		return nil, err
	}
	return snap.view(), nil
}

// Create mints a fresh NamespaceID for name and publishes a new registry
// version. This is the Managed policy (ADR-96 K2): namespaces are created
// explicitly, never implicitly on first Put. It rejects an empty or
// oversized name and a name that already exists (errs.ErrAlreadyExists).
func (r *Registry) Create(ctx context.Context, name string) (NamespaceID, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	snap, err := r.load(ctx)
	if err != nil {
		return "", err
	}
	for _, existing := range snap.Entries {
		if existing == name {
			return "", fmt.Errorf("namespace %q: %w", name, errs.ErrAlreadyExists)
		}
	}
	id := NamespaceID(uuid.NewString())
	snap.Entries[id] = name
	if err := r.persist(ctx, snap); err != nil {
		return "", err
	}
	return id, nil
}

// Delete removes name from the registry and publishes a new version. It
// is the raw map operation: the "refuse to delete a non-empty namespace"
// guard (ADR-96 K3) belongs to the extension operation that can consult
// the index for membership, and is layered on top of this. Returns
// errs.ErrArtifactNotFound when name is unknown.
func (r *Registry) Delete(ctx context.Context, name string) error {
	snap, err := r.load(ctx)
	if err != nil {
		return err
	}
	var found bool
	for id, n := range snap.Entries {
		if n == name {
			delete(snap.Entries, id)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("namespace %q: %w", name, errs.ErrArtifactNotFound)
	}
	return r.persist(ctx, snap)
}

// Resolve maps a name to its NamespaceID.
func (v *View) Resolve(name string) (NamespaceID, bool) {
	id, ok := v.byName[name]
	return id, ok
}

// Bind resolves a name OR an id to an existing NamespaceID: it is the
// "scoped_namespace: name|id" lookup. A value matching a name returns its
// id; a value matching an existing id returns it unchanged; anything that
// resolves to no existing namespace returns false (the caller fails).
func (v *View) Bind(nameOrID string) (NamespaceID, bool) {
	if id, ok := v.byName[nameOrID]; ok {
		return id, true
	}
	if _, ok := v.byID[NamespaceID(nameOrID)]; ok {
		return NamespaceID(nameOrID), true
	}
	return "", false
}

// Name maps a NamespaceID back to its name (e.g. to label Walk results).
func (v *View) Name(id NamespaceID) (string, bool) {
	name, ok := v.byID[id]
	return name, ok
}

// List returns every namespace, unordered.
func (v *View) List() []Entry {
	out := make([]Entry, 0, len(v.byID))
	for id, name := range v.byID {
		out = append(out, Entry{ID: id, Name: name})
	}
	return out
}

// snapshot is the on-disk form of the registry.
type snapshot struct {
	Entries map[NamespaceID]string `json:"entries"` // id → name
}

func (r *Registry) load(ctx context.Context) (snapshot, error) {
	if r.sys == nil {
		return snapshot{}, errRegistryUnbound
	}
	rh, err := r.sys.Get(ctx, registryArtifact)
	if err != nil {
		if errors.Is(err, errs.ErrArtifactNotFound) {
			return snapshot{Entries: map[NamespaceID]string{}}, nil
		}
		return snapshot{}, fmt.Errorf("namespace registry: load: %w", err)
	}
	defer rh.Close()
	body, err := io.ReadAll(rh)
	if err != nil {
		return snapshot{}, fmt.Errorf("namespace registry: read: %w", err)
	}
	var snap snapshot
	if err := json.Unmarshal(body, &snap); err != nil {
		return snapshot{}, fmt.Errorf("namespace registry: decode: %w", err)
	}
	if snap.Entries == nil {
		snap.Entries = map[NamespaceID]string{}
	}
	return snap, nil
}

func (r *Registry) persist(ctx context.Context, snap snapshot) error {
	if r.sys == nil {
		return errRegistryUnbound
	}
	body, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("namespace registry: encode: %w", err)
	}
	if err := r.sys.Put(ctx, systemstore.NamedArtifact{
		Name:    registryArtifact,
		Payload: bytes.NewReader(body),
		Keep:    systemstore.KeepVersions(registryKeep),
	}); err != nil {
		return fmt.Errorf("namespace registry: persist: %w", err)
	}
	return nil
}

func (s snapshot) view() *View {
	v := &View{
		byID:   make(map[NamespaceID]string, len(s.Entries)),
		byName: make(map[string]NamespaceID, len(s.Entries)),
	}
	for id, name := range s.Entries {
		v.byID[id] = name
		v.byName[name] = id
	}
	return v
}

func validateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("namespace: empty name")
	case len(name) > domain.MaxNamespaceLen:
		return errs.ErrNamespaceTooLong
	case name == domain.NamespaceWildcard:
		return fmt.Errorf("namespace: %q is reserved", name)
	}
	return nil
}
