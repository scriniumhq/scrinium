package namespace

import (
	"context"
	"testing"
)

func newExtension(t *testing.T) *Extension {
	t.Helper()
	e, err := New(newMemSysStore())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestExtension_Descriptor(t *testing.T) {
	e := newExtension(t)
	if got := e.Descriptor().Name; got != "namespace" {
		t.Errorf("Descriptor().Name = %q, want namespace", got)
	}
}

func TestExtension_OccupiesIndexAxis(t *testing.T) {
	e := newExtension(t)
	ci, ok := e.CustomIndex()
	if !ok {
		t.Fatal("CustomIndex() ok = false, want true")
	}
	if ci.Name() != "namespace" {
		t.Errorf("index Name = %q, want namespace", ci.Name())
	}
}

func TestExtension_NoWrapperNoAgents(t *testing.T) {
	e := newExtension(t)
	if _, ok := e.Wrapper(); ok {
		t.Error("Wrapper() ok = true, want false (namespace adds no behaviour)")
	}
	if a := e.Agents(); len(a) != 0 {
		t.Errorf("Agents() = %v, want none (namespace-sync is multistore)", a)
	}
}

func TestExtension_RegistryWired(t *testing.T) {
	e := newExtension(t)
	ctx := context.Background()

	id, err := e.Registry().Create(ctx, "docs")
	if err != nil {
		t.Fatalf("Registry().Create: %v", err)
	}
	v, err := e.Registry().Load(ctx)
	if err != nil {
		t.Fatalf("Registry().Load: %v", err)
	}
	if got, ok := v.Resolve("docs"); !ok || got != id {
		t.Errorf("Resolve(docs) = %q,%v, want %q,true", got, ok, id)
	}
}

func TestExtension_NilBacking(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Error("New(nil): want error, got nil")
	}
}
