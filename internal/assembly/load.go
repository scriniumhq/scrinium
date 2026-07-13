package assembly

import (
	"bytes"
	"context"
	"fmt"

	decl "scrinium.dev/config/declarative"

	"gopkg.in/yaml.v3"
)

// LoadYAML parses a YAML config and opens the described store,
// returning a assembled stack. The store must already exist. opts are
// the same build-time options Build accepts (e.g. WithExtension) and are
// applied on top of the parsed config.
func LoadYAML(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, decl.DecodeYAML, modeOpen, opts)
}

// LoadInitYAML parses a YAML config and creates a fresh store. Errors
// if the store already exists.
func LoadInitYAML(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, decl.DecodeYAML, modeInit, opts)
}

// LoadOrInitYAML opens the described store, creating it if absent.
func LoadOrInitYAML(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, decl.DecodeYAML, modeOpenOrInit, opts)
}

// LoadJSON parses a JSON config and opens the described store.
func LoadJSON(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, decl.DecodeJSON, modeOpen, opts)
}

// LoadInitJSON parses a JSON config and creates a fresh store.
func LoadInitJSON(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, decl.DecodeJSON, modeInit, opts)
}

// LoadOrInitJSON opens the described store, creating it if absent.
func LoadOrInitJSON(ctx context.Context, data []byte, opts ...BuildOption) (Assembly, error) {
	return loadAndBuild(ctx, data, decl.DecodeJSON, modeOpenOrInit, opts)
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

// decoderFunc decodes config bytes into a Config. The two concrete
// decoders (YAML/JSON) and detectUnmarshal's sniffing share this shape.
type decoderFunc func([]byte, *decl.Config) error

// detectUnmarshal picks JSON when the document's first non-space byte
// is '{' or '[', YAML otherwise. Used only by Explain, which is
// format-agnostic; the Load*/LoadJSON entry points are explicit.
func detectUnmarshal(data []byte) decoderFunc {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return decl.DecodeJSON
	}
	return decl.DecodeYAML
}

func loadAndBuild(ctx context.Context, data []byte, decode decoderFunc, mode buildMode, opts []BuildOption) (Assembly, error) {
	c, err := parse(data, decode)
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

func parse(data []byte, decode decoderFunc) (*decl.Config, error) {
	var c decl.Config
	if err := decode(data, &c); err != nil {
		return nil, fmt.Errorf("scrinium: parse config: %w", err)
	}
	return &c, nil
}

// prepare runs the declarative model's pre-build pipeline (Normalize +
// Validate) — shared by both Load* and Explain. The logic lives in
// package config/declarative; prepare is just the assembly-side call
// site.
func prepare(c *decl.Config) error {
	if err := c.Normalize(); err != nil {
		return fmt.Errorf("scrinium: %w", err)
	}
	return c.Validate()
}
