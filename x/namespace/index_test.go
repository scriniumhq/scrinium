package namespace

import (
	"context"
	"encoding/json"
	"testing"

	"scrinium.dev/domain"
	"scrinium.dev/engine/customindex"
)

func TestIndex_ProjectsNSID(t *testing.T) {
	e := NewIndex()
	m := domain.Manifest{
		ArtifactID: "art-1",
		Ext:        json.RawMessage(`{"nsid":"ns-uuid-1"}`),
	}
	projs, err := e.Index(context.Background(), nil, m)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(projs) != 1 {
		t.Fatalf("projections = %d, want 1", len(projs))
	}
	p := projs[0]
	if p.Pocket != customindex.PocketExt {
		t.Errorf("Pocket = %v, want PocketExt", p.Pocket)
	}
	if p.Field != "nsid" {
		t.Errorf("Field = %q, want nsid", p.Field)
	}
	if p.Value != "ns-uuid-1" {
		t.Errorf("Value = %q, want ns-uuid-1", p.Value)
	}
}

func TestIndex_SkipsWithoutNSID(t *testing.T) {
	e := NewIndex()
	cases := map[string]json.RawMessage{
		"nil ext":      nil,
		"empty object": json.RawMessage(`{}`),
		"foreign ext":  json.RawMessage(`{"kind":"scrinium.fs/v1","path":"/a"}`),
		"empty nsid":   json.RawMessage(`{"nsid":""}`),
	}
	for name, ext := range cases {
		t.Run(name, func(t *testing.T) {
			projs, err := e.Index(context.Background(), nil, domain.Manifest{ArtifactID: "x", Ext: ext})
			if err != nil {
				t.Fatalf("Index: %v", err)
			}
			if projs != nil {
				t.Errorf("projections = %v, want nil (no namespace stamp)", projs)
			}
		})
	}
}

func TestIndex_CoexistsWithOtherExtKeys(t *testing.T) {
	e := NewIndex()
	// A vfsmeta payload that also carries the namespace stamp: the index
	// reads only its own "nsid" key and ignores the rest.
	m := domain.Manifest{
		ArtifactID: "art-2",
		Ext:        json.RawMessage(`{"kind":"scrinium.fs/v1","path":"/docs/a.txt","nsid":"ns-uuid-2"}`),
	}
	projs, err := e.Index(context.Background(), nil, m)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(projs) != 1 || projs[0].Value != "ns-uuid-2" {
		t.Fatalf("projections = %v, want one nsid=ns-uuid-2", projs)
	}
}

func TestIndex_InvalidExtErrors(t *testing.T) {
	e := NewIndex()
	m := domain.Manifest{ArtifactID: "bad", Ext: json.RawMessage(`{"nsid":123}`)}
	if _, err := e.Index(context.Background(), nil, m); err == nil {
		t.Error("Index with malformed nsid: want error, got nil")
	}
}

func TestIndex_UnindexNoop(t *testing.T) {
	e := NewIndex()
	m := domain.Manifest{ArtifactID: "art-1", Ext: json.RawMessage(`{"nsid":"ns-uuid-1"}`)}
	if err := e.Unindex(context.Background(), nil, m); err != nil {
		t.Errorf("Unindex: %v", err)
	}
}

func TestIndex_Contract(t *testing.T) {
	e := NewIndex()
	if e.Name() != "namespace" {
		t.Errorf("Name = %q, want namespace", e.Name())
	}
	if e.Subscribe() != nil {
		t.Errorf("Subscribe = %v, want nil", e.Subscribe())
	}
	if err := e.Setup(context.Background(), nil, 0); err != nil {
		t.Errorf("Setup(0): %v", err)
	}
	if err := e.Setup(context.Background(), nil, 99); err == nil {
		t.Error("Setup(99): want unsupported-version error")
	}
	// Apply is unreachable for a non-subscriber and must fail loudly.
	if err := e.Apply(context.Background(), nil, customindex.EventKindManifestIndexed, customindex.EventArgs{}); err == nil {
		t.Error("Apply: want loud error for non-subscriber")
	}
	if err := e.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
