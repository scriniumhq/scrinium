package assembly

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

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

// decoderFunc decodes config bytes into a Config. The two concrete
// decoders (YAML/JSON) and detectUnmarshal's sniffing share this shape.
type decoderFunc func([]byte, *Config) error

// Strict decoding (R-b, config review): an unknown key is an error,
// not a silent no-op. A declarative file states operator intent —
// a typo (`retenton:`) or a removed key (the old
// perStageVerification) must be said out loud, or the intent silently
// diverges from reality.
func unmarshalYAML(data []byte, c *Config) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty document → zero Config, same as before
		}
		return err
	}
	return nil
}

func unmarshalJSON(data []byte, c *Config) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(c)
}

// detectUnmarshal picks JSON when the document's first non-space byte
// is '{' or '[', YAML otherwise. Used only by Explain, which is
// format-agnostic; the Load*/LoadJSON entry points are explicit.
func detectUnmarshal(data []byte) decoderFunc {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
		return unmarshalJSON
	}
	return unmarshalYAML
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

func parse(data []byte, decode decoderFunc) (*Config, error) {
	var c Config
	if err := decode(data, &c); err != nil {
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
