package namespace

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"scrinium.dev/engine/systemstore"
	"scrinium.dev/extension"
)

func mustSnapshot(t *testing.T, entries map[NamespaceID]string) []byte {
	t.Helper()
	b, err := json.Marshal(snapshot{Entries: entries})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return b
}

func validate(name string, proposed []byte) error {
	return (&Extension{}).ValidateSystemWrite(context.Background(), name, proposed, nil)
}

func TestVeto_AcceptsValidMap(t *testing.T) {
	body := mustSnapshot(t, map[NamespaceID]string{"id-1": "alpha", "id-2": "beta"})
	if err := validate(registryArtifact, body); err != nil {
		t.Errorf("valid registry rejected: %v", err)
	}
}

func TestVeto_AcceptsEmptyMap(t *testing.T) {
	if err := validate(registryArtifact, []byte(`{"entries":{}}`)); err != nil {
		t.Errorf("empty registry rejected: %v", err)
	}
}

func TestVeto_RejectsGarbage(t *testing.T) {
	if err := validate(registryArtifact, []byte("{not json")); err == nil {
		t.Error("garbage registry payload was not rejected")
	}
}

func TestVeto_RejectsDuplicateName(t *testing.T) {
	body := mustSnapshot(t, map[NamespaceID]string{"id-1": "dup", "id-2": "dup"})
	if err := validate(registryArtifact, body); err == nil {
		t.Error("duplicate name was not rejected")
	}
}

func TestVeto_RejectsInvalidName(t *testing.T) {
	body := mustSnapshot(t, map[NamespaceID]string{"id-1": ""})
	if err := validate(registryArtifact, body); err == nil {
		t.Error("empty namespace name was not rejected")
	}
}

func TestVeto_AcceptsNonRegistryName(t *testing.T) {
	// Anything outside the registry is none of the veto's business — accept
	// without even decoding.
	if err := validate("something-else", []byte("not a map")); err != nil {
		t.Errorf("non-registry write rejected: %v", err)
	}
}

func TestVeto_BlocksDirectGarbagePut(t *testing.T) {
	ctx := context.Background()
	sys := newMemSysStore()
	scoped, err := extension.NewScopedSystemStore(extensionName, sys, extension.WithValidator(&Extension{}))
	if err != nil {
		t.Fatalf("NewScopedSystemStore: %v", err)
	}

	err = scoped.Put(ctx, systemstore.NamedArtifact{
		Name:    registryArtifact,
		Payload: bytes.NewReader([]byte("{bad json")),
	})
	if err == nil {
		t.Fatal("garbage registry write was not vetoed")
	}
	if len(sys.items) != 0 {
		t.Errorf("vetoed write leaked to backing store: %v", sys.items)
	}
}

func TestVeto_CreatePassesThroughVeto(t *testing.T) {
	ctx := context.Background()
	sys := newMemSysStore()
	scoped, err := extension.NewScopedSystemStore(extensionName, sys, extension.WithValidator(&Extension{}))
	if err != nil {
		t.Fatalf("NewScopedSystemStore: %v", err)
	}
	reg := NewRegistry(scoped)

	id, err := reg.Create(ctx, "alpha")
	if err != nil {
		t.Fatalf("Create through veto: %v", err)
	}
	if id == "" {
		t.Fatal("Create returned empty id")
	}

	view, err := reg.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, ok := view.Resolve("alpha"); !ok || got != id {
		t.Errorf("alpha not persisted through veto: got=%q ok=%v", got, ok)
	}
}
