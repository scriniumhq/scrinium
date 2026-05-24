package composer

import (
	"encoding/json"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestSizeParse(t *testing.T) {
	cases := map[string]int64{
		"":         0,
		"1024":     1024,
		"1B":       1,
		"64MB":     64 * 1000 * 1000,
		"64MiB":    64 << 20,
		"16KiB":    16 << 10,
		"1GiB":     1 << 30,
		"2KB":      2000,
		"1.5MiB":   int64(1.5 * float64(1<<20)),
		"  32MiB ": 32 << 20,
	}
	for in, want := range cases {
		var s Size
		if err := s.parse(in); err != nil {
			t.Errorf("parse(%q): %v", in, err)
			continue
		}
		if s.Int64() != want {
			t.Errorf("parse(%q) = %d, want %d", in, s.Int64(), want)
		}
	}
	var bad Size
	if err := bad.parse("12 dragons"); err == nil {
		t.Error("expected error on bad unit")
	}
}

func TestDurationParse(t *testing.T) {
	cases := map[string]time.Duration{
		"":      0,
		"5m":    5 * time.Minute,
		"90d":   90 * 24 * time.Hour,
		"7y":    7 * 365 * 24 * time.Hour,
		"1h30m": 90 * time.Minute,
		"0.5d":  12 * time.Hour,
	}
	for in, want := range cases {
		var d Duration
		if err := d.parse(in); err != nil {
			t.Errorf("parse(%q): %v", in, err)
			continue
		}
		if d.Std() != want {
			t.Errorf("parse(%q) = %v, want %v", in, d.Std(), want)
		}
	}
	var bad Duration
	if err := bad.parse("yesterday"); err == nil {
		t.Error("expected error on bad duration")
	}
}

func TestPipelineStageYAML(t *testing.T) {
	var p []PipelineStage
	doc := `
- hash
- compress:
    algo: zstd
    level: 3
- crypto:
    algo: aesgcm
- hash
`
	if err := yaml.Unmarshal([]byte(doc), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p) != 4 {
		t.Fatalf("got %d stages, want 4", len(p))
	}
	if p[0].Kind != "hash" || p[3].Kind != "hash" {
		t.Errorf("bare stages: %q %q", p[0].Kind, p[3].Kind)
	}
	if p[1].Kind != "compress" || p[1].Params["algo"] != "zstd" {
		t.Errorf("compress stage: %+v", p[1])
	}
	// yaml decodes integers as int.
	if lvl, ok := p[1].Params["level"].(int); !ok || lvl != 3 {
		t.Errorf("compress level: %v (%T)", p[1].Params["level"], p[1].Params["level"])
	}
	if p[2].Kind != "crypto" || p[2].Params["algo"] != "aesgcm" {
		t.Errorf("crypto stage: %+v", p[2])
	}
}

func TestPipelineStageJSON(t *testing.T) {
	var p []PipelineStage
	doc := `["hash", {"compress": {"algo": "zstd", "level": 3}}]`
	if err := json.Unmarshal([]byte(doc), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(p) != 2 || p[0].Kind != "hash" || p[1].Kind != "compress" {
		t.Fatalf("parsed: %+v", p)
	}
	if p[1].Params["algo"] != "zstd" {
		t.Errorf("compress algo: %v", p[1].Params["algo"])
	}
}

func TestPipelineStageMultiKeyRejected(t *testing.T) {
	var p []PipelineStage
	doc := `[{"a": {}, "b": {}}]`
	if err := json.Unmarshal([]byte(doc), &p); err == nil {
		t.Error("expected error on multi-key pipeline stage")
	}
}

func TestFullConfigYAML(t *testing.T) {
	doc := `
store:
  driver: file:///data/myapp
  policy:
    encryption:
      passphrase: file:/etc/scrinium/pass
      mode: paranoid
    bundling:
      maxBundleSize: 64MB
      flushInterval: 5m
    retention: 90d
projection:
  rootView: by-date
  editing: custom
  allowRename: true
  namespace: files
`
	var c Config
	if err := yaml.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if c.Store == nil || c.Store.Driver != "file:///data/myapp" {
		t.Fatalf("store: %+v", c.Store)
	}
	enc := c.Store.Policy.Encryption
	if enc == nil || enc.Mode != "paranoid" {
		t.Fatalf("encryption: %+v", enc)
	}
	if enc.Passphrase.String() != "file:<redacted>" {
		t.Errorf("passphrase not masked: %q", enc.Passphrase.String())
	}
	if c.Store.Policy.Bundling.MaxBundleSize.Int64() != 64*1000*1000 {
		t.Errorf("bundle size: %d", c.Store.Policy.Bundling.MaxBundleSize.Int64())
	}
	if c.Store.Policy.Retention.Std() != 90*24*time.Hour {
		t.Errorf("retention: %v", c.Store.Policy.Retention.Std())
	}
	if c.Projection == nil || c.Projection.RootView != "by-date" {
		t.Fatalf("projection: %+v", c.Projection)
	}
	if c.Projection.AllowRename == nil || !*c.Projection.AllowRename {
		t.Errorf("allowRename pointer not parsed")
	}
}

func TestMultistoreConfigYAML(t *testing.T) {
	doc := `
stores:
  hot:
    driver: file:///fast/scrinium
  cold:
    driver: s3://bucket?region=us-east-1
    credentials:
      accessKeyId: env:AWS_KEY
multistore:
  routing:
    "logs/*": hot
    "*": hot
  replication:
    "important/*": [hot, cold]
`
	var c Config
	if err := yaml.Unmarshal([]byte(doc), &c); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(c.Stores) != 2 {
		t.Fatalf("stores: %d", len(c.Stores))
	}
	if c.Multistore.Routing["logs/*"] != "hot" {
		t.Errorf("routing: %+v", c.Multistore.Routing)
	}
	if got := c.Multistore.Replication["important/*"]; len(got) != 2 {
		t.Errorf("replication: %+v", got)
	}
	if c.Stores["cold"].Credentials["accessKeyId"].String() != "env:<redacted>" {
		t.Errorf("cred not masked: %q", c.Stores["cold"].Credentials["accessKeyId"].String())
	}
}
