package namespace

import (
	"context"
	"encoding/json"
	"fmt"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/wrapper"
	"scrinium.dev/errs"
)

// scopedFactory builds the namespace data-plane wrapper bound to one
// NamespaceID. It is a Behavioral wrapper (ADR-75/88): it changes no blob
// physics, only where writes land and what Walk lists. The factory is
// constructed by a bound Extension (NewScoped), not blank-import
// registered, because it carries the resolved nsid as state.
type scopedFactory struct{ nsid NamespaceID }

// Wrap decorates inner so every write is stamped with the bound nsid and
// listing is confined to it.
func (f scopedFactory) Wrap(inner store.DataStore, _ wrapper.Deps) (store.DataStore, error) {
	if f.nsid == "" {
		return nil, fmt.Errorf("namespace: scoped wrapper has no bound nsid")
	}
	return &scopedStore{DataStore: inner, nsid: f.nsid}, nil
}

// Descriptor self-describes the wrapper for the Rules Engine. namespace is
// Behavioral and order-free (the open behavioral set, ADR-75).
func (f scopedFactory) Descriptor() wrapper.Descriptor {
	return wrapper.Descriptor{Name: extensionName, Class: wrapper.Behavioral}
}

var _ wrapper.Factory = scopedFactory{}

// scopedStore is a one-namespace VIEW of the data plane. It embeds the
// inner DataStore so maintenance/structural methods (Verify*, PutBlob,
// RollbackSession, Capacity) pass through unchanged, and overrides the
// client read/walk/write methods to confine them to the bound namespace:
//
//   - Put    stamps the bound nsid into Ext;
//   - Walk   yields only artifacts of the bound namespace;
//   - Get    returns ErrArtifactNotFound for an artifact outside it;
//   - Delete refuses (ErrArtifactNotFound) an artifact outside it.
//
// This is VIEW confinement, not access control: a holder of the unscoped
// store still sees everything, and RBAC stays a separate concern (M7,
// AuthGate). Within this handle the bound namespace is the whole world.
type scopedStore struct {
	store.DataStore
	nsid NamespaceID
}

// member reports whether a manifest belongs to the bound namespace. An
// artifact with no nsid stamp belongs to no scoped namespace.
func (s *scopedStore) member(m domain.Manifest) bool {
	id, ok, err := nsidOf(m.Ext)
	return err == nil && ok && id == s.nsid
}

// Put stamps the bound nsid into the artifact's Ext (merging with any
// keys other extensions placed there) and forwards to the inner store.
// The caller need not pass WithNamespace; if it set a conflicting one the
// scoped store wins — pinning is the wrapper's contract.
func (s *scopedStore) Put(ctx context.Context, a domain.Artifact, opts ...domain.PutOption) (domain.ArtifactID, error) {
	ext, err := stampNSID(a.Ext, s.nsid)
	if err != nil {
		return "", err
	}
	a.Ext = ext
	return s.DataStore.Put(ctx, a, opts...)
}

// Walk yields only artifacts of the bound namespace, ignoring the argument:
// a scoped store is the one-namespace view. It delegates to the core's
// extension-agnostic WalkByExt over the nsid projection (proj_ext) — the
// filter runs in the index, no manifest-file I/O, and the "namespace"/"nsid"
// coordinates live here in the extension, not in the core.
func (s *scopedStore) Walk(ctx context.Context, _ string, cb func(domain.Manifest) error) error {
	return s.DataStore.WalkByExt(ctx, extensionName, nsidField, string(s.nsid), cb)
}

// Get returns the artifact only if it belongs to the bound namespace;
// otherwise it is invisible from this view (ErrArtifactNotFound). The
// manifest is available without I/O, so the membership check is cheap.
func (s *scopedStore) Get(ctx context.Context, id domain.ArtifactID, opts ...domain.GetOption) (domain.ReadHandle, error) {
	h, err := s.DataStore.Get(ctx, id, opts...)
	if err != nil {
		return nil, err
	}
	if !s.member(h.Manifest()) {
		_ = h.Close()
		return nil, errs.ErrArtifactNotFound
	}
	return h, nil
}

// Delete refuses an artifact outside the bound namespace (ErrArtifactNotFound):
// a scoped handle cannot delete across namespaces. Membership is read from
// the manifest (no blob I/O) before the inner Delete.
func (s *scopedStore) Delete(ctx context.Context, id domain.ArtifactID) error {
	h, err := s.DataStore.Get(ctx, id)
	if err != nil {
		return err
	}
	member := s.member(h.Manifest())
	_ = h.Close()
	if !member {
		return errs.ErrArtifactNotFound
	}
	return s.DataStore.Delete(ctx, id)
}

var _ store.DataStore = (*scopedStore)(nil)

// stampNSID sets the "nsid" key in an artifact's Ext to id, preserving any
// other keys already present (e.g. a vfsmeta payload). An empty Ext
// becomes a fresh object carrying just the stamp. It errors if Ext is
// present but is not a JSON object.
func stampNSID(ext json.RawMessage, id NamespaceID) (json.RawMessage, error) {
	obj := map[string]json.RawMessage{}
	if len(ext) > 0 {
		if err := json.Unmarshal(ext, &obj); err != nil {
			return nil, fmt.Errorf("namespace: artifact Ext is not a JSON object: %w", err)
		}
	}
	idJSON, err := json.Marshal(string(id))
	if err != nil {
		return nil, err
	}
	obj[nsidField] = idJSON
	return json.Marshal(obj)
}
