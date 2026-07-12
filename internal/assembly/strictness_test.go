package assembly

import (
	"errors"
	"testing"

	"scrinium.dev/errs"
)

// R-a (config review): every policy feature whose wiring has not
// landed must fail fast with ErrNotImplemented instead of being
// silently ignored. maxArtifactSize graduated from this gate: it is
// enforced on the Put paths now and maps to StoreConfig (class II).
func TestGuardUnsupportedPolicy(t *testing.T) {
	cases := map[string]*Policy{
		"chunking":      {Chunking: &Chunking{MaxSize: 1 << 20}},
		"bundling":      {Bundling: &Bundling{MaxBundleSize: 1 << 24}},
		"pipeline":      {Pipeline: []PipelineStage{{Kind: "zstd"}}},
		"pipelineExtra": {PipelineExtra: []PipelineStage{{Kind: "zstd"}}},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if err := guardUnsupportedPolicy(p); !errors.Is(err, errs.ErrNotImplemented) {
				t.Errorf("want ErrNotImplemented, got %v", err)
			}
		})
	}

	if err := guardUnsupportedPolicy(nil); err != nil {
		t.Errorf("nil policy must pass, got %v", err)
	}
	wired := &Policy{DeletionPolicy: "retention", Retention: Duration(3600000000000), MaxArtifactSize: 1 << 30}
	if err := guardUnsupportedPolicy(wired); err != nil {
		t.Errorf("wired-only policy must pass, got %v", err)
	}
}

// R-b (config review): strict decoding — an unknown key (typo, removed
// key) is an error, not a silent no-op.
func TestUnmarshalYAML_UnknownKeyFails(t *testing.T) {
	doc := []byte("store:\n  driver: file:///data\n  policy:\n    retenton: 30d\n")
	var c Config
	if err := unmarshalYAML(doc, &c); err == nil {
		t.Fatal("typo key must fail strict decode")
	}
}

func TestUnmarshalYAML_RemovedKeyFails(t *testing.T) {
	// perStageVerification was removed by decision R2 — old configs
	// carrying it must now be told out loud.
	doc := []byte("store:\n  driver: file:///data\n  policy:\n    scrub:\n      every: 168h\n      perStageVerification: false\n")
	var c Config
	if err := unmarshalYAML(doc, &c); err == nil {
		t.Fatal("removed key must fail strict decode")
	}
}

func TestUnmarshalYAML_KnownKeysPass(t *testing.T) {
	doc := []byte("store:\n  driver: file:///data\n  policy:\n    deletionPolicy: retention\n    retention: 90d\n")
	var c Config
	if err := unmarshalYAML(doc, &c); err != nil {
		t.Fatalf("valid document failed: %v", err)
	}
	if c.Store == nil || c.Store.Driver != "file:///data" {
		t.Errorf("decoded config wrong: %+v", c.Store)
	}
}

func TestUnmarshalYAML_EmptyDocument(t *testing.T) {
	var c Config
	if err := unmarshalYAML(nil, &c); err != nil {
		t.Fatalf("empty document must decode to zero Config, got %v", err)
	}
}

func TestUnmarshalJSON_UnknownKeyFails(t *testing.T) {
	doc := []byte(`{"store": {"driver": "file:///data", "bogusKey": 1}}`)
	var c Config
	if err := unmarshalJSON(doc, &c); err == nil {
		t.Fatal("unknown JSON key must fail strict decode")
	}
}
