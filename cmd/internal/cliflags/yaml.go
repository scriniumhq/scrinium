package cliflags

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadYAMLInto reads a YAML file at path and unmarshals it into
// cfg. Used by every cmd's loadConfig to overlay file contents
// onto a Config that already holds defaults + env values.
//
// Generic on the Config type so the same helper serves all
// three cmd binaries; their Config types differ in surface
// fields but the YAML overlay step is identical.
func LoadYAMLInto[T any](path string, cfg *T) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return fmt.Errorf("YAML parse: %w", err)
	}
	return nil
}
