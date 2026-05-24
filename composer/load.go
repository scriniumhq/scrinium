package composer

import (
	"context"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"

	"scrinium.dev/engine/runtime"
)

// LoadYAML parses a YAML config and opens the described store,
// returning a running runtime. The store must already exist.
func LoadYAML(ctx context.Context, data []byte) (runtime.Runtime, error) {
	return loadAndBuild(ctx, data, unmarshalYAML, modeOpen)
}

// LoadInitYAML parses a YAML config and creates a fresh store. Errors
// if the store already exists.
func LoadInitYAML(ctx context.Context, data []byte) (runtime.Runtime, error) {
	return loadAndBuild(ctx, data, unmarshalYAML, modeInit)
}

// LoadOrInitYAML opens the described store, creating it if absent.
func LoadOrInitYAML(ctx context.Context, data []byte) (runtime.Runtime, error) {
	return loadAndBuild(ctx, data, unmarshalYAML, modeOpenOrInit)
}

// LoadJSON parses a JSON config and opens the described store.
func LoadJSON(ctx context.Context, data []byte) (runtime.Runtime, error) {
	return loadAndBuild(ctx, data, unmarshalJSON, modeOpen)
}

// LoadInitJSON parses a JSON config and creates a fresh store.
func LoadInitJSON(ctx context.Context, data []byte) (runtime.Runtime, error) {
	return loadAndBuild(ctx, data, unmarshalJSON, modeInit)
}

// LoadOrInitJSON opens the described store, creating it if absent.
func LoadOrInitJSON(ctx context.Context, data []byte) (runtime.Runtime, error) {
	return loadAndBuild(ctx, data, unmarshalJSON, modeOpenOrInit)
}

// Explain parses a config, resolves policy references, applies
// defaults, validates, and returns the fully-expanded config as YAML
// (secrets masked). For debugging only; the output format is not
// stabilised.
func Explain(ctx context.Context, data []byte) ([]byte, error) {
	c, err := parse(data, detectUnmarshal(data))
	if err != nil {
		return nil, err
	}
	if err := prepare(c); err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("composer.Explain: marshal: %w", err)
	}
	return out, nil
}

type unmarshalFunc func([]byte, *Config) error

func unmarshalYAML(data []byte, c *Config) error { return yaml.Unmarshal(data, c) }
func unmarshalJSON(data []byte, c *Config) error { return json.Unmarshal(data, c) }

// detectUnmarshal picks JSON when the document's first non-space byte
// is '{' or '[', YAML otherwise. Used only by Explain, which is
// format-agnostic; the Load*/LoadJSON entry points are explicit.
func detectUnmarshal(data []byte) unmarshalFunc {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return unmarshalJSON
		default:
			return unmarshalYAML
		}
	}
	return unmarshalYAML
}

func loadAndBuild(ctx context.Context, data []byte, um unmarshalFunc, mode buildMode) (runtime.Runtime, error) {
	c, err := parse(data, um)
	if err != nil {
		return nil, err
	}
	if err := prepare(c); err != nil {
		return nil, err
	}
	return build(ctx, c, mode)
}

func parse(data []byte, um unmarshalFunc) (*Config, error) {
	var c Config
	if err := um(data, &c); err != nil {
		return nil, fmt.Errorf("composer: parse config: %w", err)
	}
	return &c, nil
}

// prepare resolves policy references, applies defaults, and validates
// — the shared pre-build pipeline used by both Load* and Explain.
func prepare(c *Config) error {
	if err := resolvePolicyRefs(c); err != nil {
		return fmt.Errorf("composer: %w", err)
	}
	applyDefaults(c)
	if err := validate(c); err != nil {
		return err
	}
	return nil
}
