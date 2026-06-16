package assembly

import (
	"context"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// LoadYAML parses a YAML config and opens the described store,
// returning a assembled stack. The store must already exist. opts are
// the same build-time options Build accepts (e.g. WithExtension) and are
// applied on top of the parsed config.
func LoadYAML(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, unmarshalYAML, modeOpen, opts)
}

// LoadInitYAML parses a YAML config and creates a fresh store. Errors
// if the store already exists.
func LoadInitYAML(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, unmarshalYAML, modeInit, opts)
}

// LoadOrInitYAML opens the described store, creating it if absent.
func LoadOrInitYAML(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, unmarshalYAML, modeOpenOrInit, opts)
}

// LoadJSON parses a JSON config and opens the described store.
func LoadJSON(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, unmarshalJSON, modeOpen, opts)
}

// LoadInitJSON parses a JSON config and creates a fresh store.
func LoadInitJSON(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, unmarshalJSON, modeInit, opts)
}

// LoadOrInitJSON opens the described store, creating it if absent.
func LoadOrInitJSON(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, unmarshalJSON, modeOpenOrInit, opts)
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
		return nil, fmt.Errorf("scrinium: explain: marshal: %w", err)
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

func loadAndBuild(ctx context.Context, data []byte, um unmarshalFunc, mode buildMode, opts []BuildOption) (Assembly, error) {
	c, err := parse(data, um)
	if err != nil {
		return nil, err
	}
	// Load* is "parse, then Build": the byte-oriented entry points are
	// thin wrappers over the programmatic one. modeToPublic keeps the
	// internal enum out of Build's public signature. The mode is forced
	// here; caller opts (WithExtension, WithEventHandler, …) follow, so a
	// caller cannot override the mode the Load* variant chose.
	return Build(ctx, *c, append([]BuildOption{WithMode(modeToPublic(mode))}, opts...)...)
}

func modeToPublic(m buildMode) Mode {
	switch m {
	case modeOpen:
		return ModeOpen
	case modeInit:
		return ModeInit
	default:
		return ModeOpenOrInit
	}
}

func parse(data []byte, um unmarshalFunc) (*Config, error) {
	var c Config
	if err := um(data, &c); err != nil {
		return nil, fmt.Errorf("scrinium: parse config: %w", err)
	}
	return &c, nil
}

// prepare resolves policy references, applies defaults, and validates
// — the shared pre-build pipeline used by both Load* and Explain.
func prepare(c *Config) error {
	if err := resolvePolicyRefs(c); err != nil {
		return fmt.Errorf("scrinium: %w", err)
	}
	applyDefaults(c)
	if err := validate(c); err != nil {
		return err
	}
	return nil
}
