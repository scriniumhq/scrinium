package cliflags

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadYAMLInto(t *testing.T) {
	type cfg struct {
		Name  string `yaml:"name"`
		Count int    `yaml:"count"`
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(path, []byte("name: hello\ncount: 42\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var c cfg
	if err := LoadYAMLInto(path, &c); err != nil {
		t.Fatalf("LoadYAMLInto: %v", err)
	}
	if c.Name != "hello" || c.Count != 42 {
		t.Errorf("got %+v, want {Name: hello, Count: 42}", c)
	}

	// Missing file
	if err := LoadYAMLInto(filepath.Join(dir, "nope.yaml"), &c); err == nil {
		t.Error("expected error on missing file")
	}

	// Bad YAML
	if err := os.WriteFile(path, []byte("name: : :\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadYAMLInto(path, &c); err == nil {
		t.Error("expected error on malformed YAML")
	}
}
