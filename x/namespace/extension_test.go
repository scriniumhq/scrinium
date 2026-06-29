package namespace

import (
	"context"
	"testing"

	"scrinium.dev/extension"
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
	if ci.Name() != "scrinium.namespace" {
		t.Errorf("index Name = %q, want scrinium.namespace", ci.Name())
	}
}

func TestExtension_UnboundHasOpenWrapperNoAgents(t *testing.T) {
	e := newExtension(t)
	if _, ok := e.Wrapper(); !ok {
		t.Error("Wrapper() ok = false, want true (open wrapper is mandatory, ADR-99)")
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

// NewExtension builds with no store handle; its registry is unusable until
// the assembler delivers the scoped store through UseEnv (ADR-101 §4).
func TestExtension_NewExtensionDeferredRegistry(t *testing.T) {
	ns, ok := NewExtension().(*Extension)
	if !ok {
		t.Fatal("NewExtension did not return *Extension")
	}
	ctx := context.Background()

	// Before Env delivery the registry rejects writes (unbound), it does
	// not panic on a nil backing.
	if _, err := ns.Registry().Create(ctx, "docs"); err == nil {
		t.Error("Create before UseEnv: want unbound error, got nil")
	}

	// Deliver the scoped store exactly as the assembler does.
	scoped, err := extension.NewScopedSystemStore("namespace", newMemSysStore())
	if err != nil {
		t.Fatalf("NewScopedSystemStore: %v", err)
	}
	if err := ns.UseEnv(extension.Env{SystemStore: scoped}); err != nil {
		t.Fatalf("UseEnv: %v", err)
	}

	id, err := ns.Registry().Create(ctx, "docs")
	if err != nil {
		t.Fatalf("Create after UseEnv: %v", err)
	}
	v, err := ns.Registry().Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := v.Resolve("docs"); !ok || got != id {
		t.Errorf("Resolve(docs) = %q,%v, want %q,true", got, ok, id)
	}
}

func TestExtension_UseEnvNilRejected(t *testing.T) {
	ns := NewExtension().(*Extension)
	if err := ns.UseEnv(extension.Env{}); err == nil {
		t.Error("UseEnv with nil scoped store: want error, got nil")
	}
}
