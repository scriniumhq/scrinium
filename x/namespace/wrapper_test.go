package namespace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/store"
	"scrinium.dev/engine/wrapper"
	"scrinium.dev/errs"
	"scrinium.dev/extension"
)

// fakeHandle is a minimal ReadHandle exposing only a manifest; the scoped
// store reads Manifest() for membership and never reads bytes here.
type fakeHandle struct{ m domain.Manifest }

func (h fakeHandle) Read([]byte) (int, error)                              { return 0, io.EOF }
func (h fakeHandle) ReadAt([]byte, int64) (int, error)                     { return 0, io.EOF }
func (h fakeHandle) ReadAtCtx(context.Context, []byte, int64) (int, error) { return 0, io.EOF }
func (h fakeHandle) SupportsRandomAccess() bool                            { return false }
func (h fakeHandle) Close() error                                          { return nil }
func (h fakeHandle) Manifest() domain.Manifest                             { return h.m }

// fakeDataStore embeds the DataStore interface (nil) and defines only the
// methods the scoped store touches: Put, Get, Delete, Walk.
type fakeDataStore struct {
	store.DataStore
	lastExt   json.RawMessage
	manifests map[domain.ArtifactID]domain.Manifest
	walk      []domain.Manifest
	deleted   []domain.ArtifactID

	extName, field, value string // recorded WalkByExt coordinates
}

func (f *fakeDataStore) Put(_ context.Context, a domain.Artifact, _ ...domain.PutOption) (domain.ArtifactID, error) {
	f.lastExt = a.Ext
	return "art-id", nil
}

func (f *fakeDataStore) Get(_ context.Context, id domain.ArtifactID, _ ...domain.GetOption) (domain.ReadHandle, error) {
	m, ok := f.manifests[id]
	if !ok {
		return nil, errs.ErrArtifactNotFound
	}
	return fakeHandle{m: m}, nil
}

func (f *fakeDataStore) Delete(_ context.Context, id domain.ArtifactID) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeDataStore) WalkByExt(_ context.Context, extName, field, value string, cb func(domain.Manifest) error) error {
	f.extName, f.field, f.value = extName, field, value
	for _, m := range f.walk {
		if err := cb(m); err != nil {
			return err
		}
	}
	return nil
}

func mfst(nsid string) domain.Manifest {
	if nsid == "" {
		return domain.Manifest{}
	}
	return domain.Manifest{Ext: json.RawMessage(`{"nsid":"` + nsid + `"}`)}
}

func wrap(t *testing.T, nsid NamespaceID, inner store.DataStore) store.DataStore {
	t.Helper()
	s, err := scopedFactory{nsid: nsid}.Wrap(inner, wrapper.Deps{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	return s
}

func TestScopedStore_PutStampsNSID(t *testing.T) {
	inner := &fakeDataStore{}
	s := wrap(t, "ns-1", inner)
	if _, err := s.Put(context.Background(), domain.Artifact{}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var got struct {
		NSID string `json:"nsid"`
	}
	if err := json.Unmarshal(inner.lastExt, &got); err != nil {
		t.Fatalf("decode stamped ext: %v", err)
	}
	if got.NSID != "ns-1" {
		t.Errorf("stamped nsid = %q, want ns-1", got.NSID)
	}
}

func TestScopedStore_PutMergesExistingExt(t *testing.T) {
	inner := &fakeDataStore{}
	s := wrap(t, "ns-1", inner)
	a := domain.Artifact{Ext: json.RawMessage(`{"vfsmeta":{"version":1,"path":"a"}}`)}
	if _, err := s.Put(context.Background(), a); err != nil {
		t.Fatalf("Put: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(inner.lastExt, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var nsid string
	if err := json.Unmarshal(got["nsid"], &nsid); err != nil {
		t.Fatalf("decode nsid: %v", err)
	}
	if nsid != "ns-1" {
		t.Errorf("nsid = %q, want ns-1", nsid)
	}
	if _, ok := got["vfsmeta"]; !ok {
		t.Errorf("foreign vfsmeta key lost: %s", inner.lastExt)
	}
}

func TestScopedStore_PutRejectsNonObjectExt(t *testing.T) {
	inner := &fakeDataStore{}
	s := wrap(t, "ns-1", inner)
	a := domain.Artifact{Ext: json.RawMessage(`"a string, not an object"`)}
	if _, err := s.Put(context.Background(), a); err == nil {
		t.Error("Put with non-object Ext: want error")
	}
}

func TestScopedStore_WalkDelegatesToNSIDProjection(t *testing.T) {
	// The index (here the fake) does the nsid filtering; the scoped store
	// only issues the query and forwards the result set.
	inner := &fakeDataStore{walk: []domain.Manifest{mfst("ns-7"), mfst("ns-7")}}
	s := wrap(t, "ns-7", inner)
	var seen int
	err := s.Walk(context.Background(), func(domain.Manifest) error {
		seen++
		return nil
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if inner.extName != "namespace" || inner.field != "nsid" || inner.value != "ns-7" {
		t.Errorf("WalkByExt query = (%q,%q,%q), want (namespace,nsid,ns-7)", inner.extName, inner.field, inner.value)
	}
	if seen != 2 {
		t.Errorf("forwarded %d manifests, want 2 (index result set passes through)", seen)
	}
}

func TestScopedStore_GetGatesByMembership(t *testing.T) {
	inner := &fakeDataStore{manifests: map[domain.ArtifactID]domain.Manifest{
		"mine":    mfst("ns-7"),
		"foreign": mfst("other"),
	}}
	s := wrap(t, "ns-7", inner)

	if _, err := s.Get(context.Background(), "mine"); err != nil {
		t.Errorf("Get(member): %v", err)
	}
	if _, err := s.Get(context.Background(), "foreign"); err != errs.ErrArtifactNotFound {
		t.Errorf("Get(non-member) = %v, want ErrArtifactNotFound", err)
	}
}

func TestScopedStore_DeleteGatesByMembership(t *testing.T) {
	inner := &fakeDataStore{manifests: map[domain.ArtifactID]domain.Manifest{
		"mine":    mfst("ns-7"),
		"foreign": mfst("other"),
	}}
	s := wrap(t, "ns-7", inner)

	if err := s.Delete(context.Background(), "foreign"); err != errs.ErrArtifactNotFound {
		t.Errorf("Delete(non-member) = %v, want ErrArtifactNotFound", err)
	}
	if len(inner.deleted) != 0 {
		t.Errorf("inner Delete called for non-member: %v", inner.deleted)
	}
	if err := s.Delete(context.Background(), "mine"); err != nil {
		t.Errorf("Delete(member): %v", err)
	}
	if len(inner.deleted) != 1 || inner.deleted[0] != "mine" {
		t.Errorf("inner deleted = %v, want [mine]", inner.deleted)
	}
}

func TestScopedFactory_Descriptor(t *testing.T) {
	d := scopedFactory{nsid: "ns-1"}.Descriptor()
	if d.Name != "namespace" || d.Class != wrapper.Behavioral {
		t.Errorf("Descriptor = %+v, want {namespace, Behavioral}", d)
	}
}

func TestNewScoped_BindByName(t *testing.T) {
	mem := newMemSysStore()
	ctx := context.Background()
	seed, _ := New(mem)
	id, err := seed.Registry().Create(ctx, "docs")
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}

	e, err := NewScoped(ctx, mem, "docs")
	if err != nil {
		t.Fatalf("NewScoped(name): %v", err)
	}
	if e.bound != id {
		t.Errorf("bound = %q, want %q", e.bound, id)
	}
	if _, ok := e.Wrapper(); !ok {
		t.Error("bound extension must occupy the wrapper axis")
	}
}

func TestNewScoped_BindByID(t *testing.T) {
	mem := newMemSysStore()
	ctx := context.Background()
	seed, _ := New(mem)
	id, _ := seed.Registry().Create(ctx, "docs")

	e, err := NewScoped(ctx, mem, string(id))
	if err != nil {
		t.Fatalf("NewScoped(id): %v", err)
	}
	if e.bound != id {
		t.Errorf("bound = %q, want %q", e.bound, id)
	}
}

func TestNewScoped_FailsOnUnknown(t *testing.T) {
	mem := newMemSysStore()
	if _, err := NewScoped(context.Background(), mem, "no-such-namespace"); err == nil {
		t.Error("NewScoped with unresolvable name: want error, got nil")
	}
}

func TestNew_UnboundHasOpenWrapper(t *testing.T) {
	e, err := New(newMemSysStore())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	f, ok := e.Wrapper()
	if !ok {
		t.Fatal("unbound extension must occupy the open wrapper axis (ADR-99)")
	}
	if _, isOpen := f.(openFactory); !isOpen {
		t.Errorf("unbound wrapper = %T, want openFactory", f)
	}
}

// backedExtension builds a namespace extension whose registry is bound to
// a fresh in-memory scoped store, as the assembler would after open.
func backedExtension(t *testing.T) *Extension {
	t.Helper()
	scoped, err := extension.NewScopedSystemStore("namespace", newMemSysStore())
	if err != nil {
		t.Fatalf("NewScopedSystemStore: %v", err)
	}
	e := NewExtension().(*Extension)
	if err := e.UseEnv(extension.Env{SystemStore: scoped}); err != nil {
		t.Fatalf("UseEnv: %v", err)
	}
	return e
}

func TestDeleteNamespace_K3RefusesNonEmpty(t *testing.T) {
	e := backedExtension(t)
	ctx := context.Background()
	id, err := e.Registry().Create(ctx, "docs")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A member still carries the nsid → refuse (K3), namespace survives.
	members := &fakeDataStore{walk: []domain.Manifest{mfst(string(id))}}
	if err := e.DeleteNamespace(ctx, members, "docs"); !errors.Is(err, ErrNamespaceNotEmpty) {
		t.Fatalf("DeleteNamespace(non-empty) = %v, want ErrNamespaceNotEmpty", err)
	}
	if members.extName != "namespace" || members.field != "nsid" || members.value != string(id) {
		t.Errorf("membership query = %q/%q/%q, want namespace/nsid/%s",
			members.extName, members.field, members.value, id)
	}
	v, err := e.Registry().Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := v.Resolve("docs"); !ok {
		t.Error("namespace removed despite being non-empty")
	}

	// No members → delete proceeds.
	empty := &fakeDataStore{}
	if err := e.DeleteNamespace(ctx, empty, "docs"); err != nil {
		t.Fatalf("DeleteNamespace(empty) = %v, want nil", err)
	}
	v, err = e.Registry().Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := v.Resolve("docs"); ok {
		t.Error("empty namespace not removed")
	}
}

func TestDeleteNamespace_UnknownName(t *testing.T) {
	e := backedExtension(t)
	if err := e.DeleteNamespace(context.Background(), &fakeDataStore{}, "nope"); !errors.Is(err, errs.ErrArtifactNotFound) {
		t.Fatalf("DeleteNamespace(unknown) = %v, want ErrArtifactNotFound", err)
	}
}

func TestOpenWrapper_StampsResolvedNamespace(t *testing.T) {
	e := backedExtension(t)
	ctx := context.Background()
	id, err := e.Registry().Create(ctx, "docs")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	f, _ := e.Wrapper()
	inner := &fakeDataStore{}
	ds, err := f.Wrap(inner, wrapper.Deps{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if _, err := ds.Put(ctx, domain.Artifact{}, WithNamespace("docs")); err != nil {
		t.Fatalf("Put(WithNamespace docs): %v", err)
	}
	got, ok, err := nsidOf(inner.lastExt)
	if err != nil || !ok || got != id {
		t.Errorf("stamped nsid = %q (ok=%v err=%v), want %q", got, ok, err, id)
	}
}

func TestOpenWrapper_NoHintLeavesUnstamped(t *testing.T) {
	e := backedExtension(t)
	f, _ := e.Wrapper()
	inner := &fakeDataStore{}
	ds, _ := f.Wrap(inner, wrapper.Deps{})
	if _, err := ds.Put(context.Background(), domain.Artifact{}); err != nil {
		t.Fatalf("Put(no namespace): %v", err)
	}
	if _, ok, _ := nsidOf(inner.lastExt); ok {
		t.Error("Put without WithNamespace must leave Ext unstamped")
	}
}

func TestOpenWrapper_UnknownNamespaceErrors(t *testing.T) {
	e := backedExtension(t)
	f, _ := e.Wrapper()
	ds, _ := f.Wrap(&fakeDataStore{}, wrapper.Deps{})
	if _, err := ds.Put(context.Background(), domain.Artifact{}, WithNamespace("nope")); err == nil {
		t.Error("Put(WithNamespace unknown): want error, got nil")
	}
}

func TestScopedWrapper_RejectsWithNamespace(t *testing.T) {
	ds := wrap(t, "ns-1", &fakeDataStore{})
	_, err := ds.Put(context.Background(), domain.Artifact{}, WithNamespace("other"))
	if !errors.Is(err, ErrNamespaceConflict) {
		t.Fatalf("scoped Put(WithNamespace) = %v, want ErrNamespaceConflict", err)
	}
}
